// internal/ui/terminal-logger.go
package ui

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// sessionLogger writes a cleaned, optionally timestamped transcript of an SSH
// session to a file. It tees the raw output byte stream, strips ANSI/VT escape
// sequences, and applies carriage-return and backspace edits so each logged line
// matches what was actually visible on screen - then writes one (optionally
// timestamped) line per completed output line.
//
// It is fed from the single SSH read goroutine (sshReadLoop). Write and Close are
// mutex-guarded and idempotent; Write on a nil or closed logger is a no-op, so the
// read loop can call it without tight lifecycle coordination.
//
// Full-screen apps (vim, htop) redraw via cursor addressing the line model here
// does not track, so their output logs as noise - this is intended for line-
// oriented CLI sessions (the network use case), not TUI capture.
type sessionLogger struct {
	mu         sync.Mutex
	f          *os.File
	w          *bufio.Writer
	path       string
	timestamps bool
	closed     bool
	done       chan struct{}

	// Line-assembly state, persisted across Write calls (read chunks split
	// arbitrarily, including mid-escape and mid-line).
	line   []rune   // current logical line with cursor-overwrite applied
	cursor int      // write position within line
	esc    escState // escape-sequence parser state
}

type escState int

const (
	escNone     escState = iota
	escAfterESC          // saw ESC, awaiting the sequence type
	escCSI               // inside CSI (ESC [ ...), ends on a final byte 0x40-0x7E
	escOSC               // inside OSC/DCS/etc, ends on BEL or ST (ESC \)
	escOSCEsc            // saw ESC inside OSC, awaiting '\' for ST
)

func newSessionLogger(dir, name string, timestamps bool) (*sessionLogger, error) {
	if dir == "" {
		dir = GetLogsDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	safe := sanitizeLogName(name)
	if safe == "" {
		safe = "session"
	}
	fname := fmt.Sprintf("%s_%s.log", safe, time.Now().Format("20060102_150405"))
	full := filepath.Join(dir, fname)

	f, err := os.OpenFile(full, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}

	l := &sessionLogger{
		f:          f,
		w:          bufio.NewWriter(f),
		path:       full,
		timestamps: timestamps,
		done:       make(chan struct{}),
		line:       make([]rune, 0, 256),
	}
	go l.flushLoop() // keep the on-disk log near-live without per-line syscalls
	return l, nil
}

// Path returns the log file path (for logging/diagnostics).
func (l *sessionLogger) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Write tees a chunk of raw session output into the cleaned transcript.
func (l *sessionLogger) Write(p []byte) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}

	for i := 0; i < len(p); i++ {
		b := p[i]

		switch l.esc {
		case escAfterESC:
			switch b {
			case '[':
				l.esc = escCSI
			case ']', 'P', 'X', '^', '_': // OSC, DCS, SOS, PM, APC - ST/BEL terminated
				l.esc = escOSC
			default:
				l.esc = escNone // two-byte escape (ESC 7, ESC =, charset selects, ...)
			}
			continue
		case escCSI:
			if b >= 0x40 && b <= 0x7E { // final byte ends the sequence
				l.esc = escNone
			}
			continue
		case escOSC:
			if b == 0x07 { // BEL
				l.esc = escNone
			} else if b == 0x1b { // possible ST
				l.esc = escOSCEsc
			}
			continue
		case escOSCEsc:
			l.esc = escNone // consume the '\' (or whatever) and leave the string
			continue
		}

		// escNone
		switch b {
		case 0x1b: // ESC
			l.esc = escAfterESC
		case '\n':
			l.flushLineLocked()
		case '\r':
			l.cursor = 0 // return to column 0; subsequent runes overwrite
		case '\b', 0x7f: // backspace / DEL
			if l.cursor > 0 {
				l.cursor--
			}
		case '\t':
			l.put('\t')
		default:
			if b < 0x20 {
				continue // drop remaining C0 control characters
			}
			r, size := utf8.DecodeRune(p[i:])
			if r == utf8.RuneError && size <= 1 {
				l.put(rune(b)) // invalid/partial byte: emit as-is, advance one
			} else {
				l.put(r)
				i += size - 1
			}
		}
	}
}

func (l *sessionLogger) put(r rune) {
	if l.cursor < len(l.line) {
		l.line[l.cursor] = r // overwrite (post-\r / post-\b)
	} else {
		l.line = append(l.line, r)
	}
	l.cursor++
}

// flushLineLocked emits the current logical line. Caller must hold l.mu.
func (l *sessionLogger) flushLineLocked() {
	text := strings.TrimRight(string(l.line), " ")
	if l.timestamps {
		l.w.WriteString(time.Now().Format("2006-01-02 15:04:05 "))
	}
	l.w.WriteString(text)
	l.w.WriteByte('\n')
	l.line = l.line[:0]
	l.cursor = 0
}

func (l *sessionLogger) flushLoop() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			l.mu.Lock()
			if l.closed {
				l.mu.Unlock()
				return
			}
			l.w.Flush()
			l.mu.Unlock()
		case <-l.done:
			return
		}
	}
}

// Close flushes any pending partial line and closes the file. Idempotent.
func (l *sessionLogger) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	// Flush a trailing partial line (e.g. a prompt with no final newline).
	if text := strings.TrimRight(string(l.line), " "); text != "" {
		if l.timestamps {
			l.w.WriteString(time.Now().Format("2006-01-02 15:04:05 "))
		}
		l.w.WriteString(text)
		l.w.WriteByte('\n')
	}
	l.line = l.line[:0]
	l.cursor = 0
	l.w.Flush()
	l.f.Close()
	l.closed = true
	close(l.done)
}

// sanitizeLogName makes a session name safe for use in a filename.
func sanitizeLogName(s string) string {
	repl := func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', ' ', '\t', '\n', '\r':
			return '_'
		}
		return r
	}
	return strings.Map(repl, strings.TrimSpace(s))
}

// --- Session wiring ----------------------------------------------------------
//
// These four hang off Session rather than the render widget: the transcript is
// a property of the connection, not of the grid drawing it.

// IsLogging reports whether a transcript is currently being written.
func (s *Session) IsLogging() bool { return s.logger.Load() != nil }

// startLogger opens a new transcript. The caller ensures one is not already
// running. It returns the path so the UI can say where it went.
func (s *Session) startLogger() (string, error) {
	cfg := CurrentSettings()
	dir := cfg.LogDirectory
	if dir == "" {
		dir = GetLogsDir()
	}
	name := s.name
	if name == "" {
		name = "session"
	}
	lg, err := newSessionLogger(dir, name, cfg.TimestampLogs)
	if err != nil {
		return "", err
	}
	s.logger.Store(lg)
	return lg.Path(), nil
}

// stopLogger closes any running transcript. It is idempotent, so Close can call
// it without checking first.
func (s *Session) stopLogger() {
	if old := s.logger.Swap(nil); old != nil {
		old.Close()
	}
}

// ToggleLogging starts or stops the transcript live, returning the new state
// and a short message for a menu or dialog.
func (s *Session) ToggleLogging() (bool, string) {
	if s.IsLogging() {
		s.stopLogger()
		return false, "Session logging stopped."
	}
	p, err := s.startLogger()
	if err != nil {
		return false, fmt.Sprintf("Could not start logging: %v", err)
	}
	return true, "Logging to " + p
}
