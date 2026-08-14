// internal/term/ssh_test.go
// Behaviour tests for the SSH Transport, driven against a real in-process
// SSH server (see testserver_test.go).
package term

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// readUntil reads from s until want appears or the deadline passes. A
// terminal stream arrives in arbitrarily sized pieces, so anything that
// asserts on a single Read is asserting on packet boundaries.
func readUntil(t *testing.T, s *Session, want string, timeout time.Duration) string {
	t.Helper()

	type res struct {
		n   int
		err error
	}
	var got strings.Builder
	buf := make([]byte, 256)
	deadline := time.After(timeout)
	ch := make(chan res, 1)

	for {
		go func() {
			n, err := s.Read(buf)
			ch <- res{n, err}
		}()
		select {
		case r := <-ch:
			got.Write(buf[:r.n])
			if strings.Contains(got.String(), want) {
				return got.String()
			}
			if r.err != nil {
				t.Fatalf("read ended before %q appeared: %v (got %q)", want, r.err, got.String())
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %q (got %q)", want, got.String())
		}
	}
}

func TestOpenRequestsPTYWithCorrectGeometry(t *testing.T) {
	srv := newTestServer(t)
	c := srv.dial(t)

	s, err := Open(c, Options{Size: Size{Cols: 132, Rows: 43}})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	readUntil(t, s, "lab-r1#", 3*time.Second)

	pty, ok := srv.PTY()
	if !ok {
		t.Fatal("server never saw a pty-req")
	}
	// The whole point of this assertion: the Go API is (rows, cols) and the
	// wire is (cols, rows). A transposition compiles, connects, and only
	// shows up when a line wraps.
	if pty.Cols != 132 || pty.Rows != 43 {
		t.Errorf("pty geometry = %dx%d cols x rows, want 132x43", pty.Cols, pty.Rows)
	}
	if pty.Term != DefaultTerm {
		t.Errorf("TERM = %q, want %q", pty.Term, DefaultTerm)
	}
}

func TestOpenDefaultsSizeAndTerm(t *testing.T) {
	srv := newTestServer(t)
	c := srv.dial(t)

	// Zero size is what a UI toolkit hands you before first layout.
	s, err := Open(c, Options{Size: Size{Cols: 0, Rows: 0}, Term: ""})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	readUntil(t, s, "lab-r1#", 3*time.Second)

	pty, _ := srv.PTY()
	if int(pty.Cols) != DefaultSize.Cols || int(pty.Rows) != DefaultSize.Rows {
		t.Errorf("geometry = %dx%d, want %dx%d",
			pty.Cols, pty.Rows, DefaultSize.Cols, DefaultSize.Rows)
	}
	if pty.Term != DefaultTerm {
		t.Errorf("TERM = %q, want %q", pty.Term, DefaultTerm)
	}
}

func TestRoundTrip(t *testing.T) {
	srv := newTestServer(t)
	s, err := Open(srv.dial(t), Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	readUntil(t, s, "lab-r1#", 3*time.Second)

	if _, err := s.Write([]byte("show version\r")); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := readUntil(t, s, "show version", 3*time.Second)
	if !strings.Contains(got, "show version") {
		t.Errorf("echo = %q, want it to contain the sent text", got)
	}
}

func TestResizeReachesTheServer(t *testing.T) {
	srv := newTestServer(t)
	s, err := Open(srv.dial(t), Options{Size: Size{Cols: 80, Rows: 24}})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	readUntil(t, s, "lab-r1#", 3*time.Second)

	if err := s.Resize(Size{Cols: 200, Rows: 50}); err != nil {
		t.Fatalf("resize: %v", err)
	}

	select {
	case p := <-srv.resizeCh:
		if p.Cols != 200 || p.Rows != 50 {
			t.Errorf("window-change = %dx%d cols x rows, want 200x50", p.Cols, p.Rows)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server never saw a window-change")
	}

	if got := s.Size(); got != (Size{Cols: 200, Rows: 50}) {
		t.Errorf("Size() = %+v, want 200x50", got)
	}
}

func TestResizeToSameSizeIsNotSent(t *testing.T) {
	srv := newTestServer(t)
	s, err := Open(srv.dial(t), Options{Size: Size{Cols: 90, Rows: 30}})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	readUntil(t, s, "lab-r1#", 3*time.Second)

	// A drag emits these continuously; only distinct sizes should go out.
	for i := 0; i < 5; i++ {
		if err := s.Resize(Size{Cols: 90, Rows: 30}); err != nil {
			t.Fatalf("resize: %v", err)
		}
	}
	if err := s.Resize(Size{Cols: 91, Rows: 30}); err != nil {
		t.Fatalf("resize: %v", err)
	}

	select {
	case <-srv.resizeCh:
	case <-time.After(3 * time.Second):
		t.Fatal("the changed size never arrived")
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(srv.Resizes()); n != 1 {
		t.Errorf("server saw %d window-change requests, want 1", n)
	}
}

func TestResizeRejectsInvalidSize(t *testing.T) {
	srv := newTestServer(t)
	s, err := Open(srv.dial(t), Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	for _, bad := range []Size{{Cols: 0, Rows: 24}, {Cols: 80, Rows: 0}, {Cols: -1, Rows: -1}} {
		if err := s.Resize(bad); err == nil {
			t.Errorf("Resize(%+v) succeeded, want an error", bad)
		}
	}
}

func TestRemoteExitClosesDoneWithNilErr(t *testing.T) {
	srv := newTestServer(t)
	s, err := Open(srv.dial(t), Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	readUntil(t, s, "lab-r1#", 3*time.Second)
	if _, err := s.Write([]byte{ctrlExit}); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case <-s.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("Done never closed after the remote shell exited")
	}
	if err := s.Err(); err != nil {
		t.Errorf("Err() = %v, want nil for a clean remote exit", err)
	}
}

func TestRemoteFailureIsReportedThroughErr(t *testing.T) {
	srv := newTestServer(t)
	s, err := Open(srv.dial(t), Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	readUntil(t, s, "lab-r1#", 3*time.Second)
	if _, err := s.Write([]byte{ctrlFail}); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case <-s.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("Done never closed after a non-zero exit")
	}
	if s.Err() == nil {
		t.Error("Err() = nil, want the non-zero exit status reported")
	}
}

func TestReadReturnsEOFAfterRemoteExit(t *testing.T) {
	srv := newTestServer(t)
	s, err := Open(srv.dial(t), Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	readUntil(t, s, "lab-r1#", 3*time.Second)
	s.Write([]byte{ctrlExit})
	<-s.Done()

	buf := make([]byte, 64)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := s.Read(buf); err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatalf("Read error = %v, want io.EOF", err)
			}
			return
		}
	}
	t.Fatal("Read never returned EOF after the session ended")
}

func TestCloseIsIdempotentAndReportsNilErr(t *testing.T) {
	srv := newTestServer(t)
	s, err := Open(srv.dial(t), Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	readUntil(t, s, "lab-r1#", 3*time.Second)

	if err := s.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}

	select {
	case <-s.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("Done not closed after Close")
	}
	// A session the operator closed did not fail, and a UI that shows a
	// scary transport error on a deliberate disconnect is wrong.
	if err := s.Err(); err != nil {
		t.Errorf("Err() = %v, want nil after a local Close", err)
	}
}

func TestWriteAndResizeAfterCloseReturnErrClosed(t *testing.T) {
	srv := newTestServer(t)
	s, err := Open(srv.dial(t), Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	readUntil(t, s, "lab-r1#", 3*time.Second)
	s.Close()

	if _, err := s.Write([]byte("x")); !errors.Is(err, ErrClosed) {
		t.Errorf("Write after close = %v, want ErrClosed", err)
	}
	if err := s.Resize(Size{Cols: 100, Rows: 40}); !errors.Is(err, ErrClosed) {
		t.Errorf("Resize after close = %v, want ErrClosed", err)
	}
}

func TestCloseOwnsClientTearsDownTheConnection(t *testing.T) {
	srv := newTestServer(t)
	c := srv.dial(t)

	s, err := Open(c, Options{OwnsClient: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	readUntil(t, s, "lab-r1#", 3*time.Second)

	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// The client should be unusable now; a second session on it must fail.
	if _, err := c.SSH().NewSession(); err == nil {
		t.Error("client still usable after Close with OwnsClient set")
	}
}

func TestStderrIsDrainedAndRetained(t *testing.T) {
	srv := newTestServer(t)
	s, err := Open(srv.dial(t), Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	readUntil(t, s, "lab-r1#", 3*time.Second)
	if _, err := s.Write([]byte{ctrlStderr}); err != nil {
		t.Fatalf("write: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(s.Stderr(), "stderr-line") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("stderr never arrived; got %q", s.Stderr())
}

func TestEnvRejectionIsNotFatal(t *testing.T) {
	srv := newTestServer(t)
	s, err := Open(srv.dial(t), Options{
		Env: map[string]string{"REJECT_ME": "1"},
	})
	// Network gear rejects env as a matter of course; the session must
	// still come up.
	if err != nil {
		t.Fatalf("open failed on a rejected env var: %v", err)
	}
	defer s.Close()

	readUntil(t, s, "lab-r1#", 3*time.Second)
	if len(s.EnvErrors) == 0 {
		t.Error("EnvErrors is empty, want the rejection recorded")
	}
}

func TestOpenNilClient(t *testing.T) {
	if _, err := Open(nil, Options{}); err == nil {
		t.Error("Open(nil) succeeded, want an error")
	}
}

func TestSizeValid(t *testing.T) {
	cases := []struct {
		in   Size
		want bool
	}{
		{Size{80, 24}, true},
		{Size{1, 1}, true},
		{Size{0, 24}, false},
		{Size{80, 0}, false},
		{Size{-1, 24}, false},
		{Size{}, false},
	}
	for _, c := range cases {
		if got := c.in.Valid(); got != c.want {
			t.Errorf("Size{%d,%d}.Valid() = %v, want %v", c.in.Cols, c.in.Rows, got, c.want)
		}
	}
}
