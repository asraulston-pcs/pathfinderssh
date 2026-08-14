package gopyte

// internal/gopyte/resize_cursor_test.go
//
// Pins the screen-row meaning of cursor.Y across a line-count change.
//
// RowStore.Resize anchors the BOTTOM of the live window, so base moves and
// every resident row lands on a different screen row. cursor.Y is a screen
// row. If it does not move with them it comes to mean a different line of
// text, and the next thing written is painted over content that is still on
// screen -- off by exactly the number of rows the window grew.
//
// The field symptom this came from: a terminal constructed at 24 rows,
// laid out taller a moment later, with a login banner arriving in between.
// The shell's prompt landed six rows up, on top of the banner's tail.

import (
	"strings"
	"testing"
)

const resizeTestPrompt = "user@lab-host:~$ "

// bannerLines is longer than 24 rows so the screen has scrollback to expand
// into, which is what makes base move at all.
func bannerLines() []string {
	return []string{
		"Welcome to the lab host",
		"",
		" * Documentation:  https://example.invalid/docs",
		" * Management:     https://example.invalid/manage",
		" * Support:        https://example.invalid/support",
		"",
		" * Notice line one",
		"   Notice line two",
		"   Notice line three",
		"",
		"     https://example.invalid/notice",
		"",
		"Extended maintenance is not enabled.",
		"",
		"226 updates can be applied immediately.",
		"To see these additional updates run: apt list --upgradable",
		"",
		"59 additional security updates can be applied.",
		"Learn more at https://example.invalid/esm",
		"",
		"",
		"The list of available updates is more than a week old.",
		"To check for new updates run: sudo apt update",
		"New release available.",
		"Run 'do-release-upgrade' to upgrade to it.",
		"",
		"",
		"1 update could not be installed automatically.",
		"see /var/log/unattended-upgrades/unattended-upgrades.log",
		"Last login: Wed Aug 12 11:57:32 2026 from 10.0.0.2",
	}
}

func feedBanner(sc *WideCharScreen) *Stream {
	st := NewStream(sc, false)
	st.Feed(strings.Join(bannerLines(), "\r\n") + "\r\n")
	return st
}

func lastNonBlankRow(disp []string) (int, string) {
	for i := len(disp) - 1; i >= 0; i-- {
		if strings.TrimSpace(disp[i]) != "" {
			return i, disp[i]
		}
	}
	return -1, ""
}

// assertPromptFollowsBanner fails with the whole screen when the prompt is
// not the last thing on it, directly under the final banner line.
func assertPromptFollowsBanner(t *testing.T, sc *WideCharScreen) {
	t.Helper()
	disp := sc.GetDisplay()
	row, text := lastNonBlankRow(disp)
	ok := strings.HasPrefix(strings.TrimSpace(text), strings.TrimSpace(resizeTestPrompt)) &&
		row > 0 &&
		strings.HasPrefix(strings.TrimSpace(disp[row-1]), "Last login:")
	if ok {
		return
	}
	for i, l := range disp {
		t.Logf("%2d | %s", i, strings.TrimRight(l, " "))
	}
	t.Fatalf("prompt is not on the row after the banner: last non-blank row %d = %q", row, text)
}

// Baseline. No resize, so nothing can be off by anything.
func TestPromptFollowsBannerWithNoResize(t *testing.T) {
	sc := NewWideCharScreen(80, 24, 1000)
	st := feedBanner(sc)
	st.Feed(resizeTestPrompt)
	assertPromptFollowsBanner(t, sc)
}

// The field case: banner at the construction size, then the widget is laid
// out taller, then the prompt arrives.
func TestPromptFollowsBannerAfterTheScreenGrew(t *testing.T) {
	sc := NewWideCharScreen(80, 24, 1000)
	st := feedBanner(sc)
	sc.Resize(80, 30)
	st.Feed(resizeTestPrompt)
	assertPromptFollowsBanner(t, sc)
}

// The same fault in the other direction. Less visible in the field because
// a shrink usually leaves the cursor at the bottom row either way, so this
// puts it mid-screen first.
func TestPromptFollowsBannerAfterTheScreenShrank(t *testing.T) {
	sc := NewWideCharScreen(80, 40, 1000)
	st := feedBanner(sc)
	sc.Resize(80, 24)
	st.Feed(resizeTestPrompt)
	assertPromptFollowsBanner(t, sc)
}

// Growing with nothing in scrollback: base is already at origin and cannot
// move, so the cursor must NOT move either. This is why the delta is
// measured from the store rather than derived from the line counts.
func TestGrowingWithNoScrollbackLeavesTheCursorAlone(t *testing.T) {
	sc := NewWideCharScreen(80, 24, 1000)
	st := NewStream(sc, false)
	st.Feed("line one\r\nline two\r\n")

	_, beforeY := sc.GetCursor()
	sc.Resize(80, 40)
	_, afterY := sc.GetCursor()

	if afterY != beforeY {
		t.Fatalf("cursor row moved %d -> %d with no scrollback to expand into", beforeY, afterY)
	}
	st.Feed(resizeTestPrompt)
	disp := sc.GetDisplay()
	if got := strings.TrimSpace(disp[2]); !strings.HasPrefix(got, strings.TrimSpace(resizeTestPrompt)) {
		t.Fatalf("row 2 = %q, want the prompt", got)
	}
}

// The alternate screen keeps no scrollback, so its base never moves and a
// resize must leave the cursor row untouched.
func TestAlternateScreenResizeLeavesTheCursorRowAlone(t *testing.T) {
	sc := NewWideCharScreen(80, 24, 1000)
	st := feedBanner(sc)
	st.Feed("\x1b[?1049h") // enter alternate screen
	st.Feed("\x1b[10;1Hmid-screen")

	_, beforeY := sc.GetCursor()
	sc.Resize(80, 30)
	_, afterY := sc.GetCursor()

	if afterY != beforeY {
		t.Fatalf("alternate-screen cursor row moved %d -> %d across a resize", beforeY, afterY)
	}
}

// A resize while scrolled back must not leave the cursor adrift either:
// snapToLive runs first, so the delta is measured against the live base.
func TestResizeWhileViewingScrollbackStillLandsThePrompt(t *testing.T) {
	sc := NewWideCharScreen(80, 24, 1000)
	st := feedBanner(sc)
	sc.ScrollUp(5)
	sc.Resize(80, 30)
	st.Feed(resizeTestPrompt)
	assertPromptFollowsBanner(t, sc)
}
