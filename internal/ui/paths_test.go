// internal/ui/paths_test.go
package ui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory in this environment")
	}

	cases := []struct{ in, want string }{
		{"~", home},
		{"~/pf_maps/site1", filepath.Join(home, "pf_maps/site1")},
		{"  ~/x.json  ", filepath.Join(home, "x.json")},
		{"/abs/path", "/abs/path"},
		{"relative/path", "relative/path"},
		{"", ""},
		// Another user's home needs the user database. Left alone on
		// purpose, so it fails as a plain missing path rather than
		// resolving to something surprising.
		{"~someone/x", "~someone/x"},
		// Only a LEADING tilde counts.
		{"/tmp/~/x", "/tmp/~/x"},
	}
	for _, c := range cases {
		if got := ExpandHome(c.in); got != c.want {
			t.Errorf("ExpandHome(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
