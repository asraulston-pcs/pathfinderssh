// internal/ui/terminal_cursorkeys_test.go
//
// DECCKM: the six keys with two encodings.
//
// One file, four tests, because this bug is invisible until somebody opens vim
// over a real link and then reports it as "vim is broken" — there is no error
// anywhere in the stack, and the failure surfaces as vim executing the two
// bytes after an ESC it did not recognise.
//
// These feed real escape sequences through a real gopyte.Stream into a real
// screen and then press real keys, so the mode parser is part of what is under
// test rather than something the test asserts around.
package ui

import (
	"testing"

	"fyne.io/fyne/v2"
	fynetest "fyne.io/fyne/v2/test"
)

// feed drives bytes into the widget's screen the way the transport would.
func feed(t *testing.T, w *NativeTerminalWidget, s string) {
	t.Helper()
	w.stream.Feed(s)
}

// sent captures what a key press would put on the wire, without a transport.
func sent(t *testing.T, w *NativeTerminalWidget, key fyne.KeyName) string {
	t.Helper()
	var got []byte
	w.writeOverride = func(b []byte) {
		got = append(got, b...)
	}
	w.TypedKey(&fyne.KeyEvent{Name: key})
	return string(got)
}

func newCursorKeyWidget(t *testing.T) *NativeTerminalWidget {
	t.Helper()
	// The view contract: a widget is built AFTER an app exists, because
	// Fyne resolves the theme through the current one. Without this the
	// TextGrid renderer nil-derefs and the panic names a layout function.
	fynetest.NewTempApp(t)

	w := NewNativeTerminalWidget()
	if w.screen == nil || w.stream == nil {
		t.Fatal("widget built without a screen")
	}
	return w
}

// The default. Nothing has asked for anything, so CSI is what goes out.
func TestCursorKeysAreCSIUntilTheRemoteAsksOtherwise(t *testing.T) {
	w := newCursorKeyWidget(t)

	for _, tc := range []struct {
		key  fyne.KeyName
		want string
	}{
		{fyne.KeyUp, "\x1b[A"},
		{fyne.KeyDown, "\x1b[B"},
		{fyne.KeyRight, "\x1b[C"},
		{fyne.KeyLeft, "\x1b[D"},
		{fyne.KeyHome, "\x1b[H"},
		{fyne.KeyEnd, "\x1b[F"},
	} {
		if got := sent(t, w, tc.key); got != tc.want {
			t.Errorf("%s sent %q, want %q", tc.key, got, tc.want)
		}
	}
}

// vim's actual startup burst. After it, terminfo's promise (kcuu1=\EOA) is
// what the application is reading, so SS3 is the only correct answer.
func TestVimStartupSwitchesTheCursorKeysToSS3(t *testing.T) {
	w := newCursorKeyWidget(t)
	feed(t, w, "\x1b[?1049h\x1b[22;0;0t\x1b[>4;2m\x1b[?1h\x1b=")

	if !w.applicationCursorKeys() {
		t.Fatal("DECCKM did not take effect")
	}

	for _, tc := range []struct {
		key  fyne.KeyName
		want string
	}{
		{fyne.KeyUp, "\x1bOA"},
		{fyne.KeyDown, "\x1bOB"},
		{fyne.KeyRight, "\x1bOC"},
		{fyne.KeyLeft, "\x1bOD"},
		{fyne.KeyHome, "\x1bOH"},
		{fyne.KeyEnd, "\x1bOF"},
	} {
		if got := sent(t, w, tc.key); got != tc.want {
			t.Errorf("%s sent %q, want %q", tc.key, got, tc.want)
		}
	}

	// And back on exit, or the shell prompt after quitting vim gets SS3 it
	// never asked for.
	feed(t, w, "\x1b[?1l")
	if w.applicationCursorKeys() {
		t.Fatal("DECCKM survived its own reset")
	}
	if got := sent(t, w, fyne.KeyLeft); got != "\x1b[D" {
		t.Errorf("after reset, Left sent %q, want CSI", got)
	}
}

// The obvious wrong generalisation, pinned. These keys have ONE encoding, and
// routing them through the mode would break them in exactly the applications
// that set it.
func TestOnlyTheCursorKeysFollowTheMode(t *testing.T) {
	w := newCursorKeyWidget(t)
	feed(t, w, "\x1b[?1h")

	for _, tc := range []struct {
		key  fyne.KeyName
		want string
	}{
		{fyne.KeyPageUp, "\x1b[5~"},
		{fyne.KeyPageDown, "\x1b[6~"},
		{fyne.KeyDelete, "\x1b[3~"},
	} {
		if got := sent(t, w, tc.key); got != tc.want {
			t.Errorf("%s sent %q, want %q unchanged by DECCKM", tc.key, got, tc.want)
		}
	}
}

// The alternate screen and DECCKM are independent. Full-screen applications
// use one without the other, so a shortcut that read either from the other
// would be wrong for half of them.
func TestTheAlternateScreenAndDECCKMDoNotImplyEachOther(t *testing.T) {
	w := newCursorKeyWidget(t)

	feed(t, w, "\x1b[?1049h")
	if w.applicationCursorKeys() {
		t.Error("entering the alternate screen turned DECCKM on")
	}

	feed(t, w, "\x1b[?1049l\x1b[?1h")
	if !w.applicationCursorKeys() {
		t.Fatal("DECCKM did not take effect on its own")
	}
	if w.screen.IsUsingAlternate() {
		t.Error("DECCKM put the screen into the alternate buffer")
	}
}
