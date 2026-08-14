// internal/netexec/clean.go
// Output normalization and deterministic cleaning.
//
// Port of reachssh's _strip_echo_and_prompt: remove the command echo from
// the first line and the trailing prompt from the last, so captured device
// output diffs cleanly — no phantom first/last-line changes between
// snapshots. Normalize additionally flattens the escape-sequence and CR
// noise that raw PTY streams carry, since this stack deliberately has no
// terminal emulator to absorb it.
package netexec

import (
	"regexp"
	"strings"
)

// ansiRe matches CSI sequences (colors, cursor moves), OSC sequences
// (window titles), and stray single-character escapes.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[@-_]`)

// Normalize strips ANSI escape sequences, resolves CR handling (CRLF ->
// LF, then a lone CR keeps only the text after it — the overwrite
// semantics pagers and progress lines rely on), and drops NUL/BEL bytes.
func Normalize(s string) string {
	s = ansiRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if strings.ContainsRune(s, '\r') {
		lines := strings.Split(s, "\n")
		for i, line := range lines {
			if j := strings.LastIndexByte(line, '\r'); j >= 0 {
				lines[i] = line[j+1:]
			}
		}
		s = strings.Join(lines, "\n")
	}
	s = strings.ReplaceAll(s, "\x00", "")
	s = strings.ReplaceAll(s, "\x07", "")
	return s
}

// StripEchoAndPrompt removes the echoed command from the head of output and
// the prompt line from its tail. Conservative on the echo side: the first
// line is dropped only when it actually corresponds to the sent command
// (equal, or ends with it after the device re-wrapped/prefixed it).
func StripEchoAndPrompt(raw, cmd string, prompt *regexp.Regexp) string {
	lines := strings.Split(raw, "\n")

	// Trailing prompt.
	for len(lines) > 0 {
		last := strings.TrimSpace(lines[len(lines)-1])
		if last == "" || prompt.MatchString(last) {
			lines = lines[:len(lines)-1]
			continue
		}
		break
	}

	// Leading echo. Devices may echo "cmd", "prompt cmd", or a wrapped
	// form; treat a first line that equals or ends with the command as
	// echo. Some stacks emit a leading blank line first — skip those too.
	cmdTrim := strings.TrimSpace(cmd)
	for len(lines) > 0 {
		first := strings.TrimSpace(lines[0])
		if first == "" {
			lines = lines[1:]
			continue
		}
		if first == cmdTrim || strings.HasSuffix(first, cmdTrim) {
			lines = lines[1:]
		}
		break
	}

	return strings.TrimRight(strings.Join(lines, "\n"), " \t\n")
}
