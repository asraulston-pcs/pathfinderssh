// internal/term/term.go
// The interactive terminal transport layer.
//
// This is a sibling of netexec, not a layer on it. Both consume sshcore, and
// the difference is the whole point:
//
//   - netexec drives a PTY with prompt detection, disables paging, cleans
//     ANSI out of the stream, and hands back settled command output. It is
//     an automation client.
//   - term hands the caller a raw byte stream in both directions and never
//     interprets it. No prompt regex, no paging, no cleaning. Interpretation
//     belongs to the VT engine above, which is the only thing that can do it
//     correctly for an interactive session.
//
// Transport is the seam the UI sees. SSH satisfies it here; a serial console
// satisfies it without an SSH connection existing at all, which is why the
// interface is byte-level and carries Resize rather than anything
// connection-shaped.
package term

import (
	"io"

	"golang.org/x/crypto/ssh"
)

// Size is a terminal window in character cells.
type Size struct {
	Cols int
	Rows int
}

// Valid reports whether s is usable. Zero or negative dimensions are the
// common symptom of asking a UI toolkit for a size before it has laid out,
// and pushing them at a server produces either an error or a 0x0 PTY that
// silently renders nothing.
func (s Size) Valid() bool { return s.Cols > 0 && s.Rows > 0 }

// DefaultSize is used when no size is supplied. 80x24 is the size every
// network OS assumes when it has not been told otherwise.
var DefaultSize = Size{Cols: 80, Rows: 24}

// DefaultTerm is the TERM value requested when none is supplied.
//
// xterm-256color rather than vt100 (which is what netexec asks for): the
// theme registry above this layer is built on 256-colour SGR, and a device
// told it is talking to a vt100 is entitled to withhold them. Devices that
// do not recognise the value fall back to dumb behaviour rather than
// failing the PTY request.
const DefaultTerm = "xterm-256color"

// DefaultModes are the POSIX terminal modes requested with the PTY.
//
// ECHO is on because in an interactive session the *remote* echoes typed
// characters; a client that echoes locally as well double-prints everything.
// The speeds are advisory and exist because some servers reject a mode list
// that omits them.
var DefaultModes = ssh.TerminalModes{
	ssh.ECHO:          1,
	ssh.TTY_OP_ISPEED: 115200,
	ssh.TTY_OP_OSPEED: 115200,
}

// Transport is an interactive terminal connection: bytes in, bytes out, plus
// the two things a byte stream cannot express on its own — the window size,
// and the fact that the far end has gone away.
//
// Read blocks until data arrives and returns io.EOF once the session has
// ended. Write sends keystrokes. Neither interprets anything.
type Transport interface {
	io.ReadWriteCloser

	// Resize informs the far end that the window changed. It is safe to
	// call concurrently with Read and Write, and returns an error once the
	// session has ended.
	Resize(Size) error

	// Done is closed when the session ends, whether by remote exit, error,
	// or a local Close. A UI watches this to show a disconnect without
	// having to poll a Read.
	Done() <-chan struct{}

	// Err reports why the session ended. It is only meaningful once Done
	// is closed, and is nil when the session ended cleanly or was closed
	// locally.
	Err() error
}
