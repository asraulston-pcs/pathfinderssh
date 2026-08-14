// internal/ui/settingsstore.go
// Reading and writing the application settings file.
//
// # Why a JSON file and not Fyne's Preferences
//
// fyne.Preferences is the toolkit's own answer and it was rejected for a
// reason that is structural rather than aesthetic: it requires app.NewWithID,
// and every front end here calls app.New(). Switching them all would change
// where the toolkit stores state on three platforms in exchange for a key/value
// API that cannot express a struct, and would tie a settings file to a Fyne
// version. This package already reads and writes JSON in GetAppHome() for
// themes; capturerun.Profiles already established the file shape for a
// parameter store. This is the third user of a pattern, not a new mechanism.
//
// # What is in the file and what is not
//
// Only the Settings struct: the values that are true for the application no
// matter which session is in front. Per-session overrides live in the session
// YAML, run parameters live with their launch dialogs, and credentials live in
// the vault. Nothing secret is written here, which is what lets the file be
// something a person can read, diff and check into their own dotfiles.
//
// # Failure stances
//
// A missing file is a first run, not an error -- Load returns the defaults.
// A corrupt file IS an error, and Load returns the defaults alongside it: the
// application still opens with a working configuration, and the caller gets
// something to show rather than silently reverting a person's settings and
// then overwriting them on the next Save.
package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// SettingsFileName is the settings file's name inside GetAppHome().
const SettingsFileName = "settings.json"

// settingsFileVersion is stamped on write and ignored on read. It exists so a
// future format change can be recognised rather than guessed at; a reader that
// does not check it is still better off than a file that cannot say what it is.
const settingsFileVersion = 1

// settingsFile is the on-disk envelope. The settings are nested rather than at
// the top level so the version can never collide with a setting name.
type settingsFile struct {
	Version  int      `json:"version"`
	Settings Settings `json:"settings"`
}

// SettingsPath is the default location of the settings file.
func SettingsPath() string {
	return filepath.Join(GetAppHome(), SettingsFileName)
}

// LoadSettings reads path, normalizing whatever it finds.
//
// It returns usable settings in every case, including the error ones, so a
// caller can treat the error as something to report rather than as something
// to handle.
func LoadSettings(path string) (Settings, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Defaults(), nil
		}
		return Defaults(), fmt.Errorf("read settings %s: %w", path, err)
	}
	if len(raw) == 0 {
		return Defaults(), nil
	}

	var f settingsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return Defaults(), fmt.Errorf("settings file %s is corrupt: %w", path, err)
	}
	// Normalized rather than the raw struct: a hand-edited file is the
	// expected way this arrives with a font size of 400 in it.
	return f.Settings.Normalized(), nil
}

// SaveSettings writes s to path, creating the directory if it is missing.
//
// The write is atomic -- a temporary file and a rename -- because the
// alternative is a truncated settings file after a crash mid-write, which
// reads as corrupt on the next start and loses everything rather than the one
// change being made.
func SaveSettings(path string, s Settings) error {
	out, err := json.MarshalIndent(settingsFile{
		Version:  settingsFileVersion,
		Settings: s.Normalized(),
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	out = append(out, '\n')

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create settings directory: %w", err)
		}
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("replace settings %s: %w", path, err)
	}
	return nil
}
