// internal/ui/pasteconfirm.go
//
// The arithmetic and the vocabulary behind the multi-line paste confirmation.
//
// No toolkit import, by the same rule as shellmodel.go and vaultmodel.go: the
// part that decides WHAT the dialog says is the part worth testing, and it can
// be compiled and run without a display. terminal_paste.go holds the widgets.
//
// # Line endings come first
//
// Everything here counts, estimates and previews ALREADY-NORMALIZED content,
// i.e. after normalizeNewlines has made CR the only ending. Counting the
// clipboard's own endings would mean the number in the dialog and the number of
// commands the device sees could disagree on a block copied from Windows.
package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/scottpeterman/pathfinderssh/internal/sessions"
)

// normalizeNewlines converts every line ending to a bare CR, which is exactly
// what pressing Enter sends (see terminal_events.go, fyne.KeyReturn -> "\r").
//
// Without this, text copied on macOS or Linux pastes as LF and text copied on
// Windows pastes as CRLF, so typing a config block and pasting the same block
// put DIFFERENT bytes on the wire. The symptom is never an error — it is only
// ever "the device behaved strangely".
//
// CRLF collapses to ONE CR rather than two endings. Two would submit a blank
// line after every command, which on a device CLI re-prints the prompt and
// reads as the paste stuttering.
func normalizeNewlines(s string) string {
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "\r\n", "\r")
	return strings.ReplaceAll(s, "\n", "\r")
}

// countPasteLines counts lines in already-normalized content. A trailing CR
// does not add a phantom empty line: "conf t\r" is one line, the same as
// typing it.
func countPasteLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\r")
	if !strings.HasSuffix(s, "\r") {
		n++
	}
	return n
}

// pasteReviewMaxLines bounds what the review pane is given. The pane exists so
// a large block CAN be read, so this is high enough that a real config block is
// never cut; it is here because a runaway clipboard should not build a grid of
// a hundred thousand rows behind a dialog somebody is about to cancel.
const pasteReviewMaxLines = 2000

// pasteReviewText turns normalized content into what the review pane displays:
// CR back to LF so a line-oriented widget sees lines, and a bound on the count.
// The bool reports whether anything was left out, so the caller can say so
// rather than silently showing a truncated block as if it were the whole paste.
func pasteReviewText(s string) (string, bool) {
	body := strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(body, "\n")
	if len(lines) <= pasteReviewMaxLines {
		return body, false
	}
	kept := lines[:pasteReviewMaxLines]
	return strings.Join(kept, "\n"), true
}

// pasteTargetLabel names the device a paste is about to reach.
//
// It shows the NAME and the ADDRESS because on a tree built by importing a
// crawl those routinely disagree — the crawler addresses by IP and keeps the
// reported name for display — so naming one leaves the question the dialog
// exists to answer only half answered. A node with no name of its own is not
// printed twice.
func pasteTargetLabel(n sessions.Node) string {
	n = n.Normalize()
	label := n.Label()
	addr := nodeAddress(n)
	if addr == "" || addr == label {
		return label
	}
	// A node with no name of its own falls back to its host, so the label IS
	// the address with the port missing. Print the fuller one alone rather
	// than the same host twice.
	if label == n.Host || label == n.SerialPort {
		return addr
	}
	return label + " (" + addr + ")"
}

// nodeAddress renders the dial target of a node in the form a person would
// recognise from the session form.
func nodeAddress(n sessions.Node) string {
	if n.Transport == sessions.TransportSerial {
		return n.SerialPort
	}
	if n.Host == "" {
		return ""
	}
	if n.Port > 0 {
		return n.Host + ":" + strconv.Itoa(n.Port)
	}
	return n.Host
}

// --- the two pacing selectors -------------------------------------------
//
// Both follow the same rule and it is not cosmetic: the widget is read by its
// SELECTED STRING and mapped back through the *FromLabel functions, never by
// index. An index is silently wrong the first time an option is inserted, and
// the failure lands on whichever setting moved down a slot.

// pasteNoDelayLabel and pasteFullSpeedLabel are the "off" entries. They are
// spelled out rather than shown as 0, because a zero in a speed box reads as an
// unset field rather than as a choice.
const (
	pasteNoDelayLabel   = "No delay"
	pasteFullSpeedLabel = "Full speed"
)

var basePasteDelaysMs = []int{0, 10, 25, 50, 100, 250, 500}

var basePasteBauds = []int{0, 1200, 2400, 4800, 9600, 19200, 38400, 57600, 115200}

// pasteDelayLabel renders a per-line delay. Anything at or below zero is "no
// delay": a negative value is the session-level "never pace this device", and
// by the time it reaches the dialog it has already been resolved to off.
func pasteDelayLabel(ms int) string {
	if ms <= 0 {
		return pasteNoDelayLabel
	}
	return strconv.Itoa(ms) + " ms"
}

// pasteDelayFromLabel maps a selected label back to milliseconds. An
// unrecognised label yields zero, which is the safe direction: a paste that
// fails to pace is visible immediately, where one that paces when it should not
// looks like a hung session.
func pasteDelayFromLabel(s string) int {
	s = strings.TrimSpace(s)
	if s == pasteNoDelayLabel || s == "" {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(s, "ms")))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// pasteBaudLabel renders a console line speed.
func pasteBaudLabel(baud int) string {
	if baud <= 0 {
		return pasteFullSpeedLabel
	}
	return strconv.Itoa(baud) + " baud"
}

// pasteBaudFromLabel maps a selected label back to a line speed.
func pasteBaudFromLabel(s string) int {
	s = strings.TrimSpace(s)
	if s == pasteFullSpeedLabel || s == "" {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(s, "baud")))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// pasteDelayChoices returns the option list and the entry to preselect.
//
// A value that is not in the shipped list is FOLDED IN rather than dropped. It
// can only be there because somebody set it deliberately in the session file,
// and a trip through this dialog must not be how they discover it was quietly
// rounded to the nearest option.
func pasteDelayChoices(current int) (opts []string, selected string) {
	return pasteChoices(basePasteDelaysMs, current, pasteDelayLabel)
}

// pasteBaudChoices is pasteDelayChoices for the console speed selector.
func pasteBaudChoices(current int) (opts []string, selected string) {
	return pasteChoices(basePasteBauds, current, pasteBaudLabel)
}

func pasteChoices(base []int, current int, label func(int) string) ([]string, string) {
	if current < 0 {
		current = 0
	}
	values := make([]int, 0, len(base)+1)
	inserted := false
	for _, v := range base {
		if !inserted && current < v {
			values = append(values, current)
			inserted = true
		}
		if v == current {
			inserted = true
		}
		values = append(values, v)
	}
	if !inserted {
		values = append(values, current)
	}

	opts := make([]string, 0, len(values))
	for _, v := range values {
		opts = append(opts, label(v))
	}
	return opts, label(current)
}

// --- the estimate --------------------------------------------------------

// pasteEstimate is how long the paste will take to drain, given the pacing
// about to be applied. It is the number that turns "this will take a while"
// into a decision, and it is why the dropdowns are in the dialog at all: a
// four-minute answer is the moment to change the speed, not after.
//
// It mirrors what writePaste actually does — chunked bytes within a line at the
// derated console rate, plus the inter-line delay after every line but the
// last — so the two cannot drift without a test noticing.
func pasteEstimate(content string, delayMs, baud int) time.Duration {
	if content == "" {
		return 0
	}
	var d time.Duration

	if chunk, gap := pastePacing(baud); chunk > 0 {
		chunks := (len(content) + chunk - 1) / chunk
		if chunks > 0 {
			d += time.Duration(chunks-1) * gap
		}
	}
	if delayMs > 0 {
		gaps := countPasteLines(content) - 1
		if strings.HasSuffix(content, "\r") {
			gaps++
		}
		if gaps > 0 {
			d += time.Duration(gaps) * time.Duration(delayMs) * time.Millisecond
		}
	}
	return d
}

// formatPasteDuration renders an estimate at the precision a person can act on.
// Sub-second is "immediate" rather than "0.4s", because the difference between
// nothing and half a second is not what this line is for.
func formatPasteDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return "immediate"
	case d < time.Minute:
		return fmt.Sprintf("about %.0fs", d.Seconds())
	default:
		m := int(d / time.Minute)
		s := int((d % time.Minute) / time.Second)
		if s == 0 {
			return fmt.Sprintf("about %dm", m)
		}
		return fmt.Sprintf("about %dm %ds", m, s)
	}
}

// Dialog geometry. The width is fixed because a config line is the widest
// thing in the dialog and 80-ish columns of monospace is what it is; the height
// follows the content so a three-line paste does not open a half-screen box
// with an empty pane in it.
const (
	pasteDialogWidth     = 760
	pasteDialogChrome    = 250 // headline, selectors, summary, checkbox, buttons
	pasteDialogRowHeight = 15
	pasteDialogMinRows   = 4
	pasteDialogMaxRows   = 24
)

// pasteDialogHeight sizes the dialog to the block it is showing, between a
// floor that keeps the pane a pane and a ceiling that keeps the dialog on the
// screen. Past the ceiling the Scroll does the rest.
func pasteDialogHeight(lines int) float32 {
	rows := lines
	if rows < pasteDialogMinRows {
		rows = pasteDialogMinRows
	}
	if rows > pasteDialogMaxRows {
		rows = pasteDialogMaxRows
	}
	return pasteDialogChrome + float32(rows)*pasteDialogRowHeight
}

// pasteConfirmHeadline is the one line above the review pane. It answers the
// two questions the dialog exists for — how much, and to what — and nothing
// else, because everything below it is the content itself.
func pasteConfirmHeadline(target string, lines int) string {
	unit := "lines"
	if lines == 1 {
		unit = "line"
	}
	if target == "" {
		return fmt.Sprintf("Send %d %s?", lines, unit)
	}
	return fmt.Sprintf("Send %d %s to %s?", lines, unit, target)
}

// pastePacingSummary is the status line under the two selectors: what the
// current selection means for this paste, in the order somebody reads it.
func pastePacingSummary(content string, delayMs, baud int, bracketed bool) string {
	// Bracketed paste drops the inter-line delay -- the markers exist to keep
	// the block together, so nothing may be inserted BETWEEN its lines. Byte
	// pacing still applies. The estimate has to say the same thing writePaste
	// does, or the dropdown promises a delay that is never applied.
	effective := delayMs
	if bracketed {
		effective = 0
	}
	est := formatPasteDuration(pasteEstimate(content, effective, baud))
	switch {
	case bracketed && delayMs > 0:
		return est + " — the remote asked for bracketed paste, so the line delay does not apply"
	case bracketed:
		return est + " — sent as one block (bracketed paste)"
	case delayMs <= 0 && baud <= 0:
		return est + " — one write, no pacing"
	default:
		return est + " — Ctrl+C aborts a paste in progress"
	}
}
