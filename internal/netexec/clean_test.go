// internal/netexec/clean_test.go
// Unit tests for Normalize, StripEchoAndPrompt, and prompt detection.
package netexec

import (
	"regexp"
	"testing"
)

var prompt = regexp.MustCompile(DefaultPromptRegex)

func TestNormalizeStripsAnsiAndCR(t *testing.T) {
	in := "\x1b[32mlab-r1#\x1b[0m\r\nline1\r\nprogress 10%\rprogress done\r\n"
	want := "lab-r1#\nline1\nprogress done\n"
	if got := Normalize(in); got != want {
		t.Fatalf("Normalize:\n got %q\nwant %q", got, want)
	}
}

func TestStripEchoAndPrompt(t *testing.T) {
	raw := "show version\nIOS XE 17.9.4a\nuptime 12 weeks\nlab-r1#"
	got := StripEchoAndPrompt(raw, "show version", prompt)
	want := "IOS XE 17.9.4a\nuptime 12 weeks"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestStripEchoWithPromptPrefix(t *testing.T) {
	// Some stacks echo "prompt command" on one line.
	raw := "lab-r1# show clock\n10:15:03.201 UTC Tue Jul 28 2026\nlab-r1#"
	got := StripEchoAndPrompt(raw, "show clock", prompt)
	want := "10:15:03.201 UTC Tue Jul 28 2026"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestStripKeepsNonEchoFirstLine(t *testing.T) {
	// First line is real output, not echo — must survive.
	raw := "hostname lab-r1\nlab-r1#"
	got := StripEchoAndPrompt(raw, "show run | include hostname", prompt)
	want := "hostname lab-r1"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestPromptDetection(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"lab-r1#", true},
		{"lab-r1>", true},
		{"lab-r1(config)#", true},
		{"user@lab-host:~$", true},
		{"lab-fw1 %", false}, // space before % — not a prompt tail
		{"lab-sw2% ", true},  // trailing space is fine
		{"building configuration...", false},
		{"", false},
		{"output line\nlab-r1#", true},
		{"lab-r1#\nmore output still streaming", false},
	}
	for _, c := range cases {
		if got := endsAtPrompt(c.text, prompt); got != c.want {
			t.Errorf("endsAtPrompt(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}
