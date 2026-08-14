// internal/help/help.go
//
// The user documentation, compiled into the binary.
//
// # Why this is a file and not a server
//
// internal/mapweb is a listener because it has to be: the viewer needs a
// payload that changes per map, and clicking a node has to reach back into the
// application to open a session. Help has neither. It is static text that is
// the same on every machine, so it is rendered to ONE self-contained HTML file
// and handed to the browser as file://. No socket, no port, nothing to
// firewall, and it works with the network unplugged.
//
// # Why the images are inline
//
// A help page that references images/crawl.png needs those files next to it,
// which turns "open the help" into a distribution problem: an installer that
// lays down a directory, a Store package that keeps it, an uninstall that
// removes it. Encoding each image as a data: URI removes the question. The
// rendered page has no external references of any kind -- no img src, no
// stylesheet link, no font fetch -- which is asserted by a test rather than
// hoped for, because a single relative reference that slips in works fine on
// the machine that wrote it and fails everywhere else.
//
// # Why the content is embedded as a directory
//
// //go:embed content/quickstart.md would be a build failure on a tree where
// somebody moved a file. Embedding the directory compiles either way, and the
// tests are what enforce that every topic has content. Same trade as
// internal/ui/assets.go: a missing picture costs the picture, not the build.
//
// # Toolkit-free on purpose
//
// This package renders bytes. Opening a browser is the caller's job -- see
// URL() -- so the whole of it is testable without a display, and cmd/ tools
// can dump the page without linking Fyne.
package help

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// contentFS holds the authored markdown and its images.
//
//go:embed content
var contentFS embed.FS

// contentDir is the root inside contentFS. Image destinations in the markdown
// are resolved relative to it, so images/foo.png means content/images/foo.png.
const contentDir = "content"

// Topic is the stable anchor for one section of the page.
//
// These strings are a published interface in the same way a command-line flag
// is: they end up in bookmarks, in support answers, and in the help buttons
// scattered through internal/ui. Add topics freely; renaming one silently
// breaks every deep link that ever pointed at it.
type Topic string

const (
	TopicQuickstart Topic = "quickstart"
	TopicSessions   Topic = "sessions"
	TopicVault      Topic = "vault"
	TopicCrawl      Topic = "crawl"
	TopicMap        Topic = "map"
	TopicCapture    Topic = "capture"
	TopicSearch     Topic = "search"
	TopicSettings   Topic = "settings"
)

// topic is one section: an id, the title in the sidebar, and its source file.
type topic struct {
	ID    Topic
	Title string
	File  string
}

// topics is the page order, top to bottom. Quickstart is first because it is
// the only section somebody reads without being sent there by a help button.
var topics = []topic{
	{TopicQuickstart, "Quickstart", "quickstart.md"},
	{TopicSessions, "Sessions and the session tree", "sessions.md"},
	{TopicVault, "Credentials and the vault", "vault.md"},
	{TopicCrawl, "Crawl", "crawl.md"},
	{TopicMap, "The map", "map.md"},
	{TopicCapture, "Capture", "capture.md"},
	{TopicSearch, "Search", "search.md"},
	{TopicSettings, "Settings", "settings.md"},
}

// Topics returns every topic id, in page order.
func Topics() []Topic {
	out := make([]Topic, 0, len(topics))
	for _, t := range topics {
		out = append(out, t.ID)
	}
	return out
}

// Known reports whether id is a topic this build ships.
//
// Callers wiring a help button should assert this in a test rather than at
// runtime: a button pointing at a topic that does not exist opens the page at
// the top, which looks like it worked.
func Known(id Topic) bool {
	for _, t := range topics {
		if t.ID == id {
			return true
		}
	}
	return false
}

// FileName is the name of the rendered page inside the help directory.
const FileName = "help.html"

// stampPrefix opens the rendered file and names the build that wrote it.
//
// The version lives in the page instead of a sidecar stamp file so there is
// exactly one artifact to write, compare, and delete. A half-written pair --
// new stamp, old HTML -- is a state nobody would think to check for.
const stampPrefix = "<!-- pathfinderssh-help "

// Ensure renders the page into dir if what is there was not written by this
// version, and returns the path to it.
//
// The common case is the second launch of a build that already opened help
// once: the file is read far enough to check the stamp and nothing is written.
// A version of "" always re-renders, which is what a developer build wants.
func Ensure(dir, version string) (string, error) {
	if dir == "" {
		return "", errors.New("help: no directory")
	}
	path := filepath.Join(dir, FileName)

	if version != "" && stampOf(path) == version {
		return path, nil
	}

	html, _, err := Render(version)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("help: %w", err)
	}
	// Write beside the target and rename, so a reader never sees a
	// half-written page and a failure leaves the previous one intact.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, html, 0o644); err != nil {
		return "", fmt.Errorf("help: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("help: %w", err)
	}
	return path, nil
}

// stampOf returns the version recorded in the file at path, or "" when the
// file is missing, unreadable, or was not written by this package.
func stampOf(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	buf := make([]byte, 256)
	n, _ := f.Read(buf)
	line := string(buf[:n])
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	if !strings.HasPrefix(line, stampPrefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, stampPrefix), "-->"))
}

// URL turns a rendered help file and a topic into something a browser can
// open.
//
// This exists because building it by hand is wrong on Windows and right
// everywhere else, which is the worst way for a bug to behave. A Windows path
// is C:\Users\you\help.html, and file://C:\Users... makes C: the HOST. The URL
// has to be file:///C:/Users/you/help.html -- three slashes, forward slashes,
// and the drive letter inside the path. url.URL does that correctly given an
// absolute path with forward slashes; string concatenation does not.
//
// An empty topic opens the page at the top.
func URL(path string, id Topic) (*url.URL, error) {
	if path == "" {
		return nil, errors.New("help: no path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("help: %w", err)
	}
	u := fileURL(filepath.ToSlash(abs))
	if id != "" {
		u.Fragment = string(id)
	}
	return u, nil
}

// fileURL builds the file:// URL for an absolute, forward-slashed path.
//
// Split out from URL so the Windows shape can be asserted on any host:
// filepath.Abs is the only part that behaves differently per platform, and it
// is not the part that gets this wrong.
func fileURL(p string) *url.URL {
	if !strings.HasPrefix(p, "/") {
		// C:/Users/... becomes /C:/Users/..., which is what puts the
		// drive letter in the path instead of making it the host.
		p = "/" + p
	}
	return &url.URL{Scheme: "file", Path: p}
}

// readContent returns one file from the embedded content directory.
func readContent(name string) ([]byte, error) {
	return fs.ReadFile(contentFS, contentDir+"/"+name)
}
