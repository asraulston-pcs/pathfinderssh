// internal/ui/shellmodel_test.go
package ui

import (
	"strings"
	"sync"
	"testing"
)

func TestTitlesAreMadeUnique(t *testing.T) {
	r := NewRegistry()
	a := r.Add(KindCrawl, "crawl")
	b := r.Add(KindCrawl, "crawl")
	c := r.Add(KindCrawl, "crawl")

	if a.Title != "crawl" || b.Title != "crawl (2)" || c.Title != "crawl (3)" {
		t.Fatalf("titles = %q %q %q", a.Title, b.Title, c.Title)
	}
	if a.ID == b.ID || b.ID == c.ID {
		t.Fatalf("ids collide: %d %d %d", a.ID, b.ID, c.ID)
	}
}

// Closing a tab and opening another must not leave a numbering hole. A shell
// where the second crawl is called "crawl (3)" reads like one went missing.
func TestTitleNumberingReusesFreedNames(t *testing.T) {
	r := NewRegistry()
	first := r.Add(KindCrawl, "crawl")
	second := r.Add(KindCrawl, "crawl")
	if !r.Remove(first.ID) {
		t.Fatal("remove reported nothing removed")
	}
	third := r.Add(KindCrawl, "crawl")
	if third.Title != "crawl" {
		t.Fatalf("after closing %q, next title = %q, want the freed name", first.Title, third.Title)
	}
	if second.Title != "crawl (2)" {
		t.Fatalf("existing instance was renamed to %q", second.Title)
	}
}

func TestEmptyBaseFallsBackToKind(t *testing.T) {
	r := NewRegistry()
	if got := r.Add(KindCapture, "").Title; got != "capture" {
		t.Fatalf("title = %q", got)
	}
}

func TestRemoveReportsWhetherAnythingWent(t *testing.T) {
	r := NewRegistry()
	in := r.Add(KindTerminal, "lab-r1")
	if !r.Remove(in.ID) {
		t.Fatal("first remove said no")
	}
	if r.Remove(in.ID) {
		t.Fatal("second remove said yes; a double close would go unnoticed")
	}
	if r.Len() != 0 {
		t.Fatalf("len = %d", r.Len())
	}
}

func TestPlacementMoves(t *testing.T) {
	r := NewRegistry()
	in := r.Add(KindTerminal, "lab-r1")
	if in.Placement != Docked {
		t.Fatalf("new instance placement = %v", in.Placement)
	}
	r.SetPlacement(in.ID, Detached)
	if r.Get(in.ID).Placement != Detached {
		t.Fatal("placement did not move")
	}
	if r.CountPlacement(Detached) != 1 || r.CountPlacement(Docked) != 0 {
		t.Fatalf("counts = %d detached, %d docked", r.CountPlacement(Detached), r.CountPlacement(Docked))
	}
}

func TestAllIsASnapshot(t *testing.T) {
	r := NewRegistry()
	r.Add(KindCrawl, "crawl")
	got := r.All()
	r.Add(KindCapture, "capture")
	if len(got) != 1 {
		t.Fatalf("earlier snapshot grew to %d", len(got))
	}
	if len(r.All()) != 2 {
		t.Fatalf("current listing = %d", len(r.All()))
	}
}

func TestSummaryNamesWhatIsOpen(t *testing.T) {
	r := NewRegistry()
	if r.Summary() != "" {
		t.Fatalf("empty registry summarised as %q", r.Summary())
	}
	r.Add(KindTerminal, "lab-r1")
	r.Add(KindTerminal, "lab-r2")
	c := r.Add(KindCrawl, "crawl")
	r.SetPlacement(c.ID, Detached)

	s := r.Summary()
	for _, want := range []string{"2 terminals", "1 crawl", "own window"} {
		if !strings.Contains(s, want) {
			t.Fatalf("summary %q missing %q", s, want)
		}
	}
}

// The whole point of the lock: a transport closing on its own goroutine asks
// what is still open while the UI goroutine is opening and closing tabs.
func TestConcurrentReadsAndWrites(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			in := r.Add(KindTerminal, "lab-r1")
			r.SetPlacement(in.ID, Detached)
			r.Remove(in.ID)
		}
	}()

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = r.All()
				_ = r.Len()
				_ = r.Summary()
				_ = r.CountKind(KindTerminal)
			}
		}()
	}
	wg.Wait()

	if r.Len() != 0 {
		t.Fatalf("len = %d after balanced add/remove", r.Len())
	}
}
