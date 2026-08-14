// internal/ui/debug.go
package ui

import (
	"fmt"
	"log"
	"os"

	"github.com/scottpeterman/pathfinderssh/internal/gopyte"
)

// debugEnabled gates verbose per-frame trace output (render-loop logs, terminal
// event traces, drag/scroll forwarding, etc). Off by default; enabled when
// TETHERSSH_DEBUG is set to a non-empty value other than "0".
//
// fmt.Printf (stdout) was used throughout the terminal widget purely for
// tracing - real terminal output is drawn to the Fyne grid, never stdout - so
// gating all of it is safe. Only the high-frequency render-loop log.Printf
// lines are gated; genuine lifecycle and error logs stay unconditional.
var debugEnabled = func() bool {
	v := os.Getenv("TETHERSSH_DEBUG")
	return v != "" && v != "0"
}()

// traceEnabled gates gopyte's per-escape-sequence parser tracing, which is
// extremely high volume (hundreds of synchronous log lines per full-screen
// frame) and will dominate any timing measurement if left on. It is gated
// SEPARATELY from debugEnabled: TETHERSSH_DEBUG enables app-level timing/trace
// (dlogf/dprintf) while leaving the parser quiet, so the render/feed path can
// be profiled cleanly. Set TETHERSSH_TRACE only when you actually want the
// per-sequence firehose.
var traceEnabled = func() bool {
	v := os.Getenv("TETHERSSH_TRACE")
	return v != "" && v != "0"
}()

func init() {
	// Keep the engine package's tracing gated independently of the app's, so
	// enabling app timing doesn't flood stderr with parser traces.
	gopyte.SetDebug(traceEnabled)
}

// dprintf is a gated fmt.Printf (stdout, no timestamp).
func dprintf(format string, args ...interface{}) {
	if debugEnabled {
		fmt.Printf(format, args...)
	}
}

// dlogf is a gated log.Printf (stderr, timestamped).
func dlogf(format string, args ...interface{}) {
	if debugEnabled {
		log.Printf(format, args...)
	}
}
