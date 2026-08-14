// internal/term/ssh.go
// SSH implementation of Transport: a PTY-backed interactive shell.
//
// The dial already happened. This takes an established sshcore.Client and
// opens one session on it, which keeps every credential, host-key and
// bastion decision in the layer that made it — the same division the crawler
// uses with DialFunc, for the same reason.
package term

import (
	"errors"
	"fmt"
	"io"
	"sync"

	"golang.org/x/crypto/ssh"

	"github.com/scottpeterman/pathfinderssh/internal/sshcore"
)

// ErrClosed is returned by Resize and Write after the session has ended.
var ErrClosed = errors.New("term: session is closed")

// stderrLimit bounds the diagnostic stderr buffer. Draining stderr is not
// optional — an SSH channel has a flow-control window per stream, and a
// stream nobody reads will fill it and then stall the *whole* channel,
// including stdout. So it is always drained; this only bounds what is kept.
const stderrLimit = 8 << 10

// Options configures a session. The zero value is valid and gets the
// defaults documented in term.go.
type Options struct {
	// Term is the TERM value to request. "" => DefaultTerm.
	Term string

	// Size is the initial window. Invalid (zero or negative) => DefaultSize.
	Size Size

	// Modes are the POSIX terminal modes. nil => DefaultModes.
	Modes ssh.TerminalModes

	// Env are environment variables to request before the shell starts.
	// Most network operating systems reject these outright, so a rejection
	// is not treated as a failure — it is reported through EnvErrors and
	// the session continues.
	Env map[string]string

	// OwnsClient makes Close tear down the underlying sshcore.Client too.
	// Set it when the client was dialed for this session and nothing else
	// shares it, which is the normal case for a terminal window. Leave it
	// false when the client is pooled.
	OwnsClient bool
}

// Session is an interactive PTY shell over SSH. It satisfies Transport.
type Session struct {
	client *sshcore.Client
	sess   *ssh.Session
	stdin  io.WriteCloser
	stdout io.Reader

	owns bool

	// mu guards closed and serialises Close against Resize. Reads and
	// writes do not take it: they go straight at the SSH channel, which is
	// already safe for concurrent use, and taking a lock around a blocking
	// Read would make Resize wait for input to arrive.
	mu     sync.Mutex
	closed bool

	// size is the last size successfully pushed, so a caller can ask what
	// the far end believes without tracking it separately.
	size Size

	done     chan struct{}
	waitErr  error
	errOnce  sync.Once
	stderrMu sync.Mutex
	stderrBu []byte

	// EnvErrors holds any environment variables the server refused. It is
	// set before Open returns and not written afterwards.
	EnvErrors []error
}

// Open requests a PTY and starts an interactive shell on c.
//
// On failure the session is torn down but c is left alone regardless of
// opt.OwnsClient — a client the caller still holds a reference to is the
// caller's to close.
func Open(c *sshcore.Client, opt Options) (*Session, error) {
	if c == nil {
		return nil, errors.New("term: nil client")
	}

	size := opt.Size
	if !size.Valid() {
		size = DefaultSize
	}
	termName := opt.Term
	if termName == "" {
		termName = DefaultTerm
	}
	modes := opt.Modes
	if modes == nil {
		modes = DefaultModes
	}

	sess, err := c.SSH().NewSession()
	if err != nil {
		return nil, fmt.Errorf("term: open session: %w", err)
	}

	s := &Session{
		client: c,
		sess:   sess,
		owns:   opt.OwnsClient,
		size:   size,
		done:   make(chan struct{}),
	}

	// Env before the PTY: a server that accepts env at all wants it before
	// the channel is turned into a terminal. Failures are collected, not
	// fatal, because network gear rejects env as a matter of course.
	for k, v := range opt.Env {
		if err := sess.Setenv(k, v); err != nil {
			s.EnvErrors = append(s.EnvErrors, fmt.Errorf("setenv %s: %w", k, err))
		}
	}

	// Pipes must be taken before Shell(): x/crypto wires them at start and
	// rejects the call afterwards.
	s.stdin, err = sess.StdinPipe()
	if err != nil {
		sess.Close()
		return nil, fmt.Errorf("term: stdin: %w", err)
	}
	s.stdout, err = sess.StdoutPipe()
	if err != nil {
		sess.Close()
		return nil, fmt.Errorf("term: stdout: %w", err)
	}
	stderr, err := sess.StderrPipe()
	if err != nil {
		sess.Close()
		return nil, fmt.Errorf("term: stderr: %w", err)
	}

	// RequestPty takes (rows, cols) — height first. Getting this backwards
	// produces a session that works until the first line wraps.
	if err := sess.RequestPty(termName, size.Rows, size.Cols, modes); err != nil {
		sess.Close()
		return nil, fmt.Errorf("term: request pty: %w", err)
	}

	if err := sess.Shell(); err != nil {
		sess.Close()
		return nil, fmt.Errorf("term: start shell: %w", err)
	}

	go s.drainStderr(stderr)
	go s.wait()

	return s, nil
}

// Read returns raw output bytes, and io.EOF once the session has ended.
func (s *Session) Read(p []byte) (int, error) { return s.stdout.Read(p) }

// Write sends raw input bytes.
func (s *Session) Write(p []byte) (int, error) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return 0, ErrClosed
	}
	return s.stdin.Write(p)
}

// Resize pushes a new window size to the far end.
func (s *Session) Resize(size Size) error {
	if !size.Valid() {
		return fmt.Errorf("term: invalid size %dx%d", size.Cols, size.Rows)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if size == s.size {
		// Toolkits emit resize events continuously during a drag. Sending
		// an unchanged size is a channel request per frame for no reason.
		return nil
	}
	// WindowChange is (height, width), matching RequestPty.
	if err := s.sess.WindowChange(size.Rows, size.Cols); err != nil {
		return fmt.Errorf("term: window change: %w", err)
	}
	s.size = size
	return nil
}

// Size reports the last size the far end was successfully told about.
func (s *Session) Size() Size {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.size
}

// Done is closed when the session ends.
func (s *Session) Done() <-chan struct{} { return s.done }

// Err reports why the session ended; meaningful only once Done is closed.
// A local Close reports nil, as does a remote shell that exited cleanly.
func (s *Session) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.waitErr
}

// Stderr returns whatever the server sent on the extended data stream. With
// a PTY this is normally empty, because the pseudo-terminal merges the two
// streams server-side. It is worth having anyway: when a session dies during
// startup, the reason often arrives here and nowhere else.
func (s *Session) Stderr() string {
	s.stderrMu.Lock()
	defer s.stderrMu.Unlock()
	return string(s.stderrBu)
}

// Close ends the session, and the underlying client when OwnsClient was set.
// It is idempotent.
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	// A locally closed session did not fail, so Err stays nil: finish()
	// runs before sess.Close() can make wait() report a transport error.
	s.finish(nil)

	var first error
	if err := s.sess.Close(); err != nil && !errors.Is(err, io.EOF) {
		first = err
	}
	if s.owns && s.client != nil {
		if err := s.client.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// wait watches for the remote end of the session going away.
func (s *Session) wait() {
	err := s.sess.Wait()
	if errors.Is(err, io.EOF) {
		err = nil
	}
	s.finish(err)
}

// finish records the terminating error and closes done, at most once.
func (s *Session) finish(err error) {
	s.errOnce.Do(func() {
		s.mu.Lock()
		s.waitErr = err
		s.mu.Unlock()
		close(s.done)
	})
}

// drainStderr keeps the extended data stream moving and retains a bounded
// prefix of it. See stderrLimit for why draining is mandatory.
func (s *Session) drainStderr(r io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			s.stderrMu.Lock()
			if room := stderrLimit - len(s.stderrBu); room > 0 {
				if n > room {
					n = room
				}
				s.stderrBu = append(s.stderrBu, buf[:n]...)
			}
			s.stderrMu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// compile-time check that the SSH session is a usable Transport.
var _ Transport = (*Session)(nil)
