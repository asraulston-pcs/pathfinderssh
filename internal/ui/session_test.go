// internal/ui/session_test.go
package ui

import (
	"strings"
	"testing"
)

func TestCompletePrefixLen(t *testing.T) {
	// U+2812 is a braille cell: 0xE2 0xA0 0x92. Splitting one across a read
	// boundary is the case that produced visible corruption in practice, so
	// each of its two possible split points is covered.
	braille := "\u2812"

	tests := []struct {
		name string
		in   []byte
		want int
	}{
		{"empty", nil, 0},
		{"pure ascii", []byte("show version"), len("show version")},
		{"complete multibyte at end", []byte("cpu " + braille), 4 + 3},
		{"split after first byte", []byte("cpu \xe2"), 4},
		{"split after second byte", []byte("cpu \xe2\xa0"), 4},
		{"two byte rune complete", []byte("caf\u00e9"), 5},
		{"two byte rune split", []byte("caf\xc3"), 3},
		{"four byte rune split", []byte("x\xf0\x9f\x92"), 1},
		{"four byte rune complete", []byte("x\U0001F4A9"), 5},
		{"all continuation bytes", []byte("\x80\x80"), 2},
		{"escape sequence intact", []byte("\x1b[2J\x1b[H"), 7},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := completePrefixLen(tc.in); got != tc.want {
				t.Errorf("completePrefixLen(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestCompletePrefixLenReassembles feeds a multi-rune string through the
// function one byte at a time, carrying the tail exactly as the read loop
// does, and checks that the concatenation of the emitted pieces is byte-equal
// to the input and that no piece ever ends mid-rune.
func TestCompletePrefixLenReassembles(t *testing.T) {
	input := "load \u2812\u2812\u2812 cpu caf\u00e9 \U0001F4A9 done"

	var carried []byte
	var out strings.Builder

	for i := 0; i < len(input); i++ {
		chunk := append(carried, input[i])
		cut := completePrefixLen(chunk)
		emitted := chunk[:cut]
		carried = append([]byte(nil), chunk[cut:]...)

		if len(emitted) > 0 {
			s := string(emitted)
			if strings.ContainsRune(s, '\uFFFD') {
				t.Fatalf("emitted piece %q contains a replacement rune", s)
			}
			out.WriteString(s)
		}
	}
	out.Write(carried)

	if out.String() != input {
		t.Errorf("reassembled %q, want %q", out.String(), input)
	}
}
