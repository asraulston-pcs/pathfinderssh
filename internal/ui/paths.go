// internal/ui/paths.go
// Application paths for the UI layer.
//
// Reduced from TetherSSH's paths.go to the two helpers this package actually
// uses. The session, settings and vault paths that lived alongside them are not
// here: the vault has its own location handling in internal/vault, and the
// session and settings layers have not been ported. Add a helper when something
// needs it, not before.
package ui

import (
	"log"
	"os"
	"path/filepath"
	"strings"
)

// AppHomeDir is the per-user application directory, under the user's home.
const AppHomeDir = ".pathfinderssh"

// GetAppHome returns the application home directory, creating it if needed. A
// home directory that cannot be determined or created falls back to the working
// directory rather than failing: losing a theme override or a transcript is not
// a reason to refuse to open a terminal.
func GetAppHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Printf("could not determine home directory: %v", err)
		return "."
	}

	appHome := filepath.Join(home, AppHomeDir)
	if err := os.MkdirAll(appHome, 0o755); err != nil {
		log.Printf("could not create %s: %v", appHome, err)
	}
	return appHome
}

// GetLogsDir returns the session-transcript directory.
func GetLogsDir() string {
	return filepath.Join(GetAppHome(), "logs")
}

// ExpandHome replaces a leading ~ with the user's home directory.
//
// A shell does this before a program ever sees the argument, so a path typed
// into a TEXT FIELD is the one place it does not happen. Go's file calls take
// "~/maps/x.json" literally, try to open a directory named "~" relative to the
// working directory, and fail with "no such file or directory" — which reads
// as a missing target rather than as an unexpanded path.
//
// internal/sshcore already did this for private-key paths, which made things
// worse rather than better: one field in the app understood ~ and the rest
// silently did not.
//
// "~user" is left alone. Resolving another user's home needs the user database
// and is not something this application has any business doing.
func ExpandHome(path string) string {
	path = strings.TrimSpace(path)
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		// Better to hand back what was typed and let the file call
		// report its own failure than to invent a path.
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}
