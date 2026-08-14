// internal/mapweb/server.go
// Loopback HTTP surface for the topology map viewer.
//
// The renderer is a browser page (Cytoscape) rather than a canvas widget, so
// this package exists to hand that page one map and to take one thing back
// from it: "connect to this node." It imports no toolkit and no crawler; the
// caller supplies bytes and a callback.
//
// The threat this guards against is not the network — the listener is bound to
// the loopback address and never leaves the machine. It is the browser: every
// other page the user has open can also reach 127.0.0.1, and a terminal
// application that will dial a host on request is worth asking. Hence four
// checks on the API, none of which cost the real page anything:
//
//   - a per-run token, required as a header (a cross-origin page cannot read
//     it out of our URL, and it is never written to disk)
//   - an Origin check, so a page that somehow knows the token still cannot
//     post from another site
//   - a Host check, which is what stops DNS rebinding — a name that resolves
//     to 127.0.0.1 arrives with the wrong Host header
//   - opaque node IDs, so the only hosts the browser can ask for are hosts
//     already in the map that is currently loaded
//
// The page itself is not gated: it carries no data, and everything it displays
// arrives through the API.
package mapweb

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"
)

// The vendor script is named a second time on purpose. The directory pattern
// alone would embed whatever is there and say nothing when the graph engine is
// absent — the build would succeed and the page would 404 at runtime, where a
// missing script reads as a MIME error rather than a missing file. Naming the
// file makes its absence a compile error instead.
//
//go:embed assets
//go:embed assets/vendor/cytoscape.min.js
var assetFS embed.FS

// tokenHeader carries the per-run token on every API request.
const tokenHeader = "X-Pathfinder-Token"

// NodeRef is one node of the loaded map, as handed to OnConnect. The ID is
// opaque and lives only as long as this map is loaded; Name and IP are what
// the caller needs to open a session.
type NodeRef struct {
	ID         string
	Name       string
	IP         string
	Platform   string
	Discovered bool
}

// Options configure a Server.
type Options struct {
	// OnConnect is called when the page asks to connect to a node. It runs
	// on the HTTP goroutine, so a GUI caller must hand off to its own
	// thread (fyne.Do) rather than touching widgets here. Nil means the
	// viewer is read-only: connect requests are refused with a message
	// saying so, which is the honest answer for a standalone harness.
	OnConnect func(NodeRef)

	// Log, when set, receives one line per refused request. Refusals are
	// the interesting events here — a silent 403 during bring-up is the
	// kind of thing that gets blamed on the browser cache.
	Log func(string, ...any)
}

// Server is a running loopback map viewer. The zero value is not usable; call
// Serve.
type Server struct {
	opts  Options
	ln    net.Listener
	srv   *http.Server
	token string
	addr  string // host:port as the browser will send it

	mu      sync.RWMutex
	mapName string
	mapData json.RawMessage
	byID    map[string]NodeRef
	ids     map[string]string // node key -> opaque id
}

// Serve binds a listener on the loopback interface and starts serving. The
// port is chosen by the OS: a fixed port is one more thing to collide with,
// and nothing outside this process needs to guess it.
func Serve(opts Options) (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen on loopback: %w", err)
	}

	tok, err := randomHex(32)
	if err != nil {
		ln.Close()
		return nil, fmt.Errorf("generate token: %w", err)
	}

	s := &Server{
		opts:  opts,
		ln:    ln,
		token: tok,
		addr:  ln.Addr().String(),
		byID:  map[string]NodeRef{},
		ids:   map[string]string{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/assets/", s.handleAsset)
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		// Nothing to serve, and a 404 in the console during bring-up is
		// one more thing to rule out before finding the real error.
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/map", s.guard(s.handleMap))
	mux.HandleFunc("/api/connect", s.guard(s.handleConnect))

	s.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

// SetMap loads a map, replacing whatever was loaded before. Every node ID from
// the previous map stops resolving at that point, which is deliberate: a
// stale page left open in another tab should not still be able to connect to a
// map the user has moved on from.
//
// name is what the page displays; it is not read as a path.
func (s *Server) SetMap(name string, data []byte) error {
	nodes, err := parseNodes(data)
	if err != nil {
		return err
	}

	byID := make(map[string]NodeRef, len(nodes))
	ids := make(map[string]string, len(nodes))
	for _, n := range nodes {
		id, err := randomHex(16)
		if err != nil {
			return fmt.Errorf("generate node id: %w", err)
		}
		n.ID = id
		byID[id] = n
		ids[n.Name] = id
	}

	raw := make(json.RawMessage, len(data))
	copy(raw, data)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.mapName = name
	s.mapData = raw
	s.byID = byID
	s.ids = ids
	return nil
}

// URL is the address to open in a browser, token included. Treat it the way
// you would treat a password: it is the credential for this run.
func (s *Server) URL() string {
	return "http://" + s.addr + "/?t=" + s.token
}

// Addr is host:port, without the token — safe to print.
func (s *Server) Addr() string { return s.addr }

// Close stops the server. Pages already open stop working; that is the point.
func (s *Server) Close() error {
	err := s.srv.Close()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// NodeCount reports how many nodes the loaded map has, for a status line.
func (s *Server) NodeCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byID)
}

// ── request guards ────────────────────────────────────────────────────

func (s *Server) logf(format string, args ...any) {
	if s.opts.Log != nil {
		s.opts.Log(format, args...)
	}
}

// guard applies the token, Origin and Host checks to an API handler. It is
// deliberately one function: a check that is applied per-handler is a check
// that gets forgotten on the next handler.
func (s *Server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.hostOK(r.Host) {
			s.logf("mapweb: refused %s %s: Host %q", r.Method, r.URL.Path, r.Host)
			http.Error(w, "bad host", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && !s.originOK(o) {
			s.logf("mapweb: refused %s %s: Origin %q", r.Method, r.URL.Path, o)
			http.Error(w, "bad origin", http.StatusForbidden)
			return
		}
		got := r.Header.Get(tokenHeader)
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			s.logf("mapweb: refused %s %s: bad or missing token", r.Method, r.URL.Path)
			http.Error(w, "bad token", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// hostOK rejects a request that reached us under any name but our own. A
// rebinding attack resolves an attacker's domain to 127.0.0.1 and gets the
// browser to send the request for us; the one thing it cannot forge is the
// Host header, which still names the attacker's domain.
func (s *Server) hostOK(host string) bool {
	if host == s.addr {
		return true
	}
	_, port, err := net.SplitHostPort(s.addr)
	if err != nil {
		return false
	}
	h, p, err := net.SplitHostPort(host)
	if err != nil || p != port {
		return false
	}
	return h == "127.0.0.1" || h == "localhost" || h == "[::1]" || h == "::1"
}

func (s *Server) originOK(origin string) bool {
	const scheme = "http://"
	if len(origin) <= len(scheme) || origin[:len(scheme)] != scheme {
		return false
	}
	return s.hostOK(origin[len(scheme):])
}

// ── handlers ──────────────────────────────────────────────────────────

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	page, err := fs.ReadFile(assetFS, "assets/index.html")
	if err != nil {
		http.Error(w, "viewer assets missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(page)
}

// contentTypes is deliberately our own table rather than mime.TypeByExtension.
// On Unix that function reads /etc/mime.types, so the type a script is served
// with depends on which packages the operator happens to have installed — and
// a browser with strict MIME checking refuses to execute a script served as
// text/plain, which presents as "cytoscape is not defined" and looks like a
// missing file rather than a header problem.
var contentTypes = map[string]string{
	".js":   "text/javascript; charset=utf-8",
	".css":  "text/css; charset=utf-8",
	".html": "text/html; charset=utf-8",
	".json": "application/json",
	".svg":  "image/svg+xml",
	".png":  "image/png",
	".txt":  "text/plain; charset=utf-8",
}

func (s *Server) handleAsset(w http.ResponseWriter, r *http.Request) {
	// path.Clean on the embed path keeps a traversal attempt from naming
	// anything outside assets/; embed.FS would refuse anyway, but the
	// refusal should not depend on that.
	name := path.Clean(strings.TrimPrefix(r.URL.Path, "/"))
	if !strings.HasPrefix(name, "assets/") {
		http.NotFound(w, r)
		return
	}

	data, err := fs.ReadFile(assetFS, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if ct, ok := contentTypes[strings.ToLower(path.Ext(name))]; ok {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

type mapPayload struct {
	Name       string            `json:"name"`
	Map        json.RawMessage   `json:"map"`
	IDs        map[string]string `json:"ids"`
	CanConnect bool              `json:"can_connect"`
}

func (s *Server) handleMap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.RLock()
	payload := mapPayload{
		Name:       s.mapName,
		Map:        s.mapData,
		IDs:        s.ids,
		CanConnect: s.opts.OnConnect != nil,
	}
	s.mu.RUnlock()

	if payload.Map == nil {
		http.Error(w, "no map loaded", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

type connectRequest struct {
	ID string `json:"id"`
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req connectRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	s.mu.RLock()
	node, ok := s.byID[req.ID]
	s.mu.RUnlock()

	if !ok {
		s.logf("mapweb: connect refused: unknown node id")
		http.Error(w, "unknown node", http.StatusNotFound)
		return
	}
	if s.opts.OnConnect == nil {
		http.Error(w, "this viewer cannot open sessions", http.StatusNotImplemented)
		return
	}

	s.opts.OnConnect(node)
	writeJSON(w, http.StatusOK, map[string]string{"status": "opening", "name": node.Name})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
