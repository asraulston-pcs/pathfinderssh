// internal/term/testserver_test.go
// A real SSH server, in process, for the terminal tests.
//
// The alternative was a fake that records method calls, which would have
// asserted that this package calls x/crypto the way the test author thinks
// x/crypto works. The interesting failures here are all wire-level — PTY
// geometry arriving transposed, a stream nobody drains stalling the channel,
// a resize sent after teardown — and none of them are visible to a fake.
//
// It is deliberately not a shipped package: it lives in _test.go so it can
// never be linked into a binary.
package term

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/scottpeterman/pathfinderssh/internal/sshcore"
)

// ptyReqPayload is the RFC 4254 pty-req body. Note the wire order: columns
// come before rows, which is the transposition of the Go API's (rows, cols).
type ptyReqPayload struct {
	Term     string
	Cols     uint32
	Rows     uint32
	WidthPx  uint32
	HeightPx uint32
	Modes    string
}

// winChPayload is the RFC 4254 window-change body, same ordering.
type winChPayload struct {
	Cols     uint32
	Rows     uint32
	WidthPx  uint32
	HeightPx uint32
}

// Control bytes the fake shell understands, so a test can drive the far end.
const (
	ctrlExit   = 0x04 // EOT: exit cleanly with status 0
	ctrlFail   = 0x05 // exit with a non-zero status
	ctrlStderr = 0x06 // emit a line on the extended data stream
)

// testServer is a single-connection SSH server that serves an echoing shell.
type testServer struct {
	Addr string

	ln net.Listener

	mu       sync.Mutex
	pty      ptyReqPayload
	ptySeen  bool
	resizes  []winChPayload
	envSeen  map[string]string
	rejected []string

	resizeCh chan winChPayload
}

// newTestServer starts a server and registers cleanup.
func newTestServer(t *testing.T) *testServer {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("host signer: %v", err)
	}

	cfg := &ssh.ServerConfig{
		PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
			return nil, nil
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	s := &testServer{
		Addr:     ln.Addr().String(),
		ln:       ln,
		envSeen:  map[string]string{},
		resizeCh: make(chan winChPayload, 16),
	}
	go s.serve(cfg)

	t.Cleanup(func() { ln.Close() })
	return s
}

func (s *testServer) serve(cfg *ssh.ServerConfig) {
	for {
		nc, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handleConn(nc, cfg)
	}
}

func (s *testServer) handleConn(nc net.Conn, cfg *ssh.ServerConfig) {
	sc, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		nc.Close()
		return
	}
	defer sc.Close()
	go ssh.DiscardRequests(reqs)

	for nch := range chans {
		if nch.ChannelType() != "session" {
			nch.Reject(ssh.UnknownChannelType, "only sessions here")
			continue
		}
		ch, chReqs, err := nch.Accept()
		if err != nil {
			return
		}
		go s.handleSession(ch, chReqs)
	}
}

func (s *testServer) handleSession(ch ssh.Channel, reqs <-chan *ssh.Request) {
	for req := range reqs {
		switch req.Type {
		case "pty-req":
			var p ptyReqPayload
			if err := ssh.Unmarshal(req.Payload, &p); err != nil {
				req.Reply(false, nil)
				continue
			}
			s.mu.Lock()
			s.pty = p
			s.ptySeen = true
			s.mu.Unlock()
			req.Reply(true, nil)

		case "window-change":
			var p winChPayload
			if err := ssh.Unmarshal(req.Payload, &p); err != nil {
				continue
			}
			s.mu.Lock()
			s.resizes = append(s.resizes, p)
			s.mu.Unlock()
			select {
			case s.resizeCh <- p:
			default:
			}

		case "env":
			var p struct{ Name, Value string }
			if err := ssh.Unmarshal(req.Payload, &p); err != nil {
				req.Reply(false, nil)
				continue
			}
			// "REJECT_ME" exists so a test can exercise the non-fatal
			// env-rejection path that real network gear takes by default.
			if p.Name == "REJECT_ME" {
				s.mu.Lock()
				s.rejected = append(s.rejected, p.Name)
				s.mu.Unlock()
				req.Reply(false, nil)
				continue
			}
			s.mu.Lock()
			s.envSeen[p.Name] = p.Value
			s.mu.Unlock()
			req.Reply(true, nil)

		case "shell":
			req.Reply(true, nil)
			go s.runShell(ch)

		default:
			req.Reply(false, nil)
		}
	}
}

// runShell echoes input back, and interprets a few control bytes so tests can
// make the far end do something other than sit there.
func (s *testServer) runShell(ch ssh.Channel) {
	io.WriteString(ch, "lab-r1#")

	buf := make([]byte, 256)
	for {
		n, err := ch.Read(buf)
		for _, b := range buf[:n] {
			switch b {
			case ctrlExit:
				ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
				ch.Close()
				return
			case ctrlFail:
				ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{7}))
				ch.Close()
				return
			case ctrlStderr:
				io.WriteString(ch.Stderr(), "stderr-line\n")
			default:
				ch.Write([]byte{b})
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return
			}
			return
		}
	}
}

// PTY returns the pty-req the server saw.
func (s *testServer) PTY() (ptyReqPayload, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pty, s.ptySeen
}

// Resizes returns every window-change the server saw, in order.
func (s *testServer) Resizes() []winChPayload {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]winChPayload, len(s.resizes))
	copy(out, s.resizes)
	return out
}

// Env returns the environment variables the server accepted.
func (s *testServer) Env() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for k, v := range s.envSeen {
		out[k] = v
	}
	return out
}

// dial connects to the test server through sshcore, so the tests exercise the
// real dial path rather than constructing an ssh.Client directly.
//
// HostKeyInsecure is correct here and only here: the server generates a fresh
// throwaway host key per test, so there is nothing a known_hosts file could
// meaningfully be checked against.
func (s *testServer) dial(t *testing.T) *sshcore.Client {
	t.Helper()

	host, port, err := net.SplitHostPort(s.Addr)
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	portNum, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}

	c, err := sshcore.Dial(sshcore.Config{
		Host:     host,
		Port:     portNum,
		Username: "lab",
		Password: "lab",
		HostKeys: sshcore.HostKeyInsecure,
	})
	if err != nil {
		t.Fatalf("dial test server: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
