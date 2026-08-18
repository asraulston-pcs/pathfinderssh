// internal/ui/shutdown.go
//
// What is still live when somebody closes the window, and what to say about
// it.
//
// The shell knows a tab exists. Only the host knows whether the thing inside
// it is doing anything -- it holds the session, or the cancel func -- so
// liveness arrives as a Busy func on the Mount and this file does nothing but
// turn those answers into a sentence. That split is what makes the wording
// testable without a display: the dialog in cmd/pathfinder is three lines, and
// everything worth getting right is here.
package ui

import "fmt"

// BusyInstance is one open applet that has something to lose. Reason is the
// host's own word for what it is doing -- "connected", "running" -- and is
// shown as given, so a new applet describes itself without editing this file.
type BusyInstance struct {
	Kind      Kind
	Title     string
	Reason    string
	Placement Placement
}

// shutdownListLimit caps the enumeration. Past this the list stops being read
// and starts being a wall, and the count in the headline is the part that
// matters anyway.
const shutdownListLimit = 8

// ShutdownPrompt is the message to show before the application closes, and
// whether to ask at all.
//
// Nothing live means no question. A confirmation that appears every single
// time is one that gets clicked through without reading, and the click-through
// habit costs exactly the crawl it was built to protect.
func ShutdownPrompt(items []BusyInstance) (string, bool) {
	if len(items) == 0 {
		return "", false
	}

	msg := busyHeadline(items) + "\n"
	for i, it := range items {
		if i == shutdownListLimit {
			msg += fmt.Sprintf("\n…and %d more", len(items)-i)
			break
		}
		msg += "\n" + it.Line()
	}

	if len(items) == 1 {
		return msg + "\n\nQuitting closes it.", true
	}
	return msg + "\n\nQuitting closes all of them.", true
}

// Line is one row of the list: what it is, what it is called, what it is
// doing, and -- when it applies -- that it is in a window of its own.
//
// The last part is not decoration. A detached instance is not in the tab
// strip, so it is precisely the one somebody looking at the main window has
// forgotten about.
func (b BusyInstance) Line() string {
	s := string(b.Kind)
	if b.Title != "" {
		s += " · " + b.Title
	}
	if b.Reason != "" {
		s += " — " + b.Reason
	}
	if b.Placement == Detached {
		s += " (own window)"
	}
	return s
}

// busyHeadline counts by kind: "2 terminals and 1 crawl are still live."
//
// The known kinds come first in a fixed order, for the same reason the toolbar
// summary fixes its order -- a line whose parts reshuffle between renders is
// read twice every time. Unlike that summary, anything left over is then
// appended rather than dropped: this headline sits directly above a list of
// the same items, and a count that disagrees with the list under it is worse
// than an unfamiliar word in it.
func busyHeadline(items []BusyInstance) string {
	counts := map[Kind]int{}
	var order []Kind
	for _, it := range items {
		if counts[it.Kind] == 0 {
			order = append(order, it.Kind)
		}
		counts[it.Kind]++
	}

	var parts []string
	seen := map[Kind]bool{}
	for _, k := range []Kind{KindTerminal, KindCrawl, KindCapture, KindSearch} {
		if n := counts[k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, plural(string(k), n)))
			seen[k] = true
		}
	}
	for _, k := range order {
		if !seen[k] {
			parts = append(parts, fmt.Sprintf("%d %s", counts[k], plural(string(k), counts[k])))
			seen[k] = true
		}
	}

	verb := "are"
	if len(items) == 1 {
		verb = "is"
	}
	return joinAnd(parts) + " " + verb + " still live."
}

// joinAnd renders a list the way a sentence does: "a", "a and b",
// "a, b and c".
func joinAnd(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	out := ""
	for i, p := range parts[:len(parts)-1] {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out + " and " + parts[len(parts)-1]
}
