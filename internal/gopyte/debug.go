package gopyte

import (
	"fmt"
	"log"
	"os"
)

// Debug gates the verbose per-frame trace output in this package (buffer
// analysis, GetDisplay traces, escape-sequence handler traces, etc). It is off
// by default and turned on when TETHERSSH_DEBUG is set to a non-empty value
// other than "0". The application can also flip it at runtime via SetDebug so a
// single switch controls tracing across both packages.
//
// In this package, fmt.Printf (stdout) and log.Printf were used exclusively for
// tracing - real screen content is delivered through the rendered grid, never
// printed - so routing all of it through these helpers is safe.
var Debug = func() bool {
	v := os.Getenv("TETHERSSH_DEBUG")
	return v != "" && v != "0"
}()

// SetDebug toggles verbose tracing at runtime.
func SetDebug(on bool) { Debug = on }

// dprintf is a gated fmt.Printf (stdout, no timestamp).
func dprintf(format string, args ...interface{}) {
	if Debug {
		fmt.Printf(format, args...)
	}
}

// dlogf is a gated log.Printf (stderr, timestamped) for trace lines that were
// previously emitted through the standard logger.
func dlogf(format string, args ...interface{}) {
	if Debug {
		log.Printf(format, args...)
	}
}
