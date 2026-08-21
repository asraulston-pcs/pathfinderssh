// internal/ui/launchstore.go
//
// The last values the launch dialogs were run with, kept across restarts.
//
// Every launch dialog already seeds itself from a `prev` the host holds for the
// life of the process, so a second crawl in one sitting is a one-click repeat.
// This file is the other half: the same values survive closing the application,
// because the second crawl is usually the next morning and retyping six seeds
// and a depth is the same work whether or not the process stayed up.
//
// # A separate file from settings.json
//
// settingsstore.go states that run parameters live with their launch dialogs
// rather than in the settings file, and that stays true. Settings are what is
// true of the application; these are what was true of the last run. Mixing them
// would mean a person who wants to check their settings into dotfiles also
// checks in last Tuesday's seed list, and a corrupt launch file would take the
// theme and font size down with it. Two files, two failure domains, same
// mechanism — the envelope, the atomic write and the failure stances are
// deliberately identical to settingsstore.go.
//
// # Nothing secret is written, and that is enforced twice
//
// Redact() strips every secret field before marshalling, and it is called by
// Save rather than left to the caller. Belt and braces, because the failure
// mode is not "the feature does not work" but "a password is on disk in
// cleartext and nobody notices for a year".
//
// The reason redaction lives here rather than as `json:"-"` tags on the structs
// is that the tags would be a claim made in five other files that this one
// depends on silently. A field added to LaunchAuth or to sessions.Node next
// year would arrive with no tag and be written. TestNoSecretReachesTheFile
// marshals a fully-populated state and searches the bytes, so that field gets
// caught here instead.
//
// VaultPath is stripped for a different reason: it is not a secret, it is
// simply recomputed. The host assigns it from runVaultPath() on every launch,
// and clearing it on the manual-credentials path is the whole point of the
// credential-source selector — persisting a stale one would be re-introducing
// that bug through the back door.
package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/scottpeterman/pathfinderssh/internal/sessions"
)

// LaunchesFileName is the launch-state file's name inside GetAppHome().
const LaunchesFileName = "launches.json"

// launchesFileVersion is stamped on write and ignored on read, for the same
// reason settingsFileVersion is.
const launchesFileVersion = 1

// LaunchState is every dialog's last values, as one saved object.
//
// QuickConnect is a sessions.Node because quick connect IS the session form
// with Ephemeral set — there is one form and one model, and this is the same
// node the host already keeps in memory between opens.
type LaunchState struct {
	Crawl        CrawlLaunch    `json:"crawl"`
	Capture      CaptureLaunch  `json:"capture"`
	Search       SearchLaunch   `json:"search"`
	Merge        MapMergeLaunch `json:"merge"`
	QuickConnect sessions.Node  `json:"quick_connect"`
}

// launchesFile is the on-disk envelope, nested so the version cannot collide
// with a field name.
type launchesFile struct {
	Version int         `json:"version"`
	State   LaunchState `json:"state"`
}

// LaunchesPath is the default location of the launch-state file.
func LaunchesPath() string {
	return filepath.Join(GetAppHome(), LaunchesFileName)
}

// Redact returns a copy with every secret field cleared, plus the vault path
// the host recomputes anyway.
//
// It is exported because the host wants it in memory too: the process-lifetime
// `prev` values are re-seeded into a form on every open, so a password typed
// once is offered back for the rest of the session. That is not a disk leak,
// but it is not what anybody asked for either.
func (s LaunchState) Redact() LaunchState {
	s.Crawl.Auth.Password = ""
	s.Crawl.Params.VaultPath = ""

	s.Capture.Auth.Password = ""
	s.Capture.Params.VaultPath = ""

	s.QuickConnect = RedactNode(s.QuickConnect)
	return s
}

// RedactNode clears the secrets on a node and on its jump host.
//
// The jump half is the one that is easy to forget: a bastion password is a
// separate field on a nested struct, and a redaction that only cleared the
// top-level one would look right in every test that did not configure a jump.
func RedactNode(n sessions.Node) sessions.Node {
	n.Password = ""
	n.KeyPassphrase = ""
	n.Jump.Password = ""
	n.Jump.KeyPassphrase = ""
	return n
}

// LoadLaunches reads path.
//
// Like LoadSettings it returns something usable in every case: a missing file
// is a first run, and a corrupt one still lets the application open with empty
// dialogs rather than refusing to start over last week's seed list.
func LoadLaunches(path string) (LaunchState, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return LaunchState{}, nil
		}
		return LaunchState{}, fmt.Errorf("read launch state %s: %w", path, err)
	}
	if len(raw) == 0 {
		return LaunchState{}, nil
	}

	var f launchesFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return LaunchState{}, fmt.Errorf("launch state file %s is corrupt: %w", path, err)
	}

	// Redacted on the way in as well as out. A file written by an older
	// build, or edited by hand, must not be able to put a password back into
	// a form — and this is cheaper than being sure no such file exists.
	return f.State.Redact(), nil
}

// SaveLaunches writes s to path, atomically, with secrets stripped.
func SaveLaunches(path string, s LaunchState) error {
	out, err := json.MarshalIndent(launchesFile{
		Version: launchesFileVersion,
		State:   s.Redact(),
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal launch state: %w", err)
	}
	out = append(out, '\n')

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create launch state directory: %w", err)
		}
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return fmt.Errorf("write launch state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("replace launch state %s: %w", path, err)
	}
	return nil
}