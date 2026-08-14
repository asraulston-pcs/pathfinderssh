// internal/ui/launchforms_test.go
package ui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckOutputPathRejectsADirectory(t *testing.T) {
	dir := t.TempDir()
	err := checkOutputPath("map output", dir)
	if err == nil {
		t.Fatal("a directory was accepted as an output path")
	}
	if got := err.Error(); got == "" || !filepath.IsAbs(dir) {
		t.Fatalf("unhelpful error: %v", got)
	}
	t.Log(err)
}

func TestCheckOutputPathAcceptsAFileAndAMissingParent(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{
		filepath.Join(dir, "map.json"),                  // does not exist yet
		filepath.Join(dir, "does", "not", "exist.json"), // parent missing: writer creates it
		"", // blank means "don't write"
	} {
		if err := checkOutputPath("map output", p); err != nil {
			t.Errorf("checkOutputPath(%q) = %v", p, err)
		}
	}
}

func TestCheckInputPathRejectsAMissingFile(t *testing.T) {
	if err := checkInputPath("device file", filepath.Join(t.TempDir(), "nope.txt")); err == nil {
		t.Fatal("a missing input file was accepted")
	}
	f := filepath.Join(t.TempDir(), "devices.txt")
	if err := os.WriteFile(f, []byte("lab-r1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkInputPath("device file", f); err != nil {
		t.Fatalf("existing file rejected: %v", err)
	}
	if err := checkInputPath("device file", ""); err != nil {
		t.Fatalf("blank rejected: %v", err)
	}
}
