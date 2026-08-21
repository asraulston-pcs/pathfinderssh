// internal/ui/launchstore_test.go
package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scottpeterman/pathfinderssh/internal/capturerun"
	"github.com/scottpeterman/pathfinderssh/internal/crawlrun"
	"github.com/scottpeterman/pathfinderssh/internal/sessions"
)

// Distinctive strings, so a byte scan of the written file cannot match one of
// them by accident.
const (
	secretPass      = "zzz-crawl-password-zzz"
	secretCapPass   = "zzz-capture-password-zzz"
	secretNodePass  = "zzz-node-password-zzz"
	secretNodePhr   = "zzz-node-passphrase-zzz"
	secretJumpPass  = "zzz-jump-password-zzz"
	secretJumpPhr   = "zzz-jump-passphrase-zzz"
	secretVaultPath = "/zzz/vault/path/zzz"
)

func fullState() LaunchState {
	return LaunchState{
		Crawl: CrawlLaunch{
			Params: crawlrun.Params{
				Seeds:       []string{"172.16.2.5", "172.16.2.6"},
				Depth:       3,
				Concurrency: 8,
				Timeout:     20 * time.Second,
				Domains:     []string{"lab.example.net"},
				VaultPath:   secretVaultPath,
				CredTags:    []string{"lab"},
				Legacy:      true,
			},
			Auth:        LaunchAuth{Username: "netops", Password: secretPass, KeyPath: "/home/lab/.ssh/id_ed25519"},
			ManualCreds: true,
			MapPath:     "/home/lab/maps/lab-map.json",
			Verbose:     true,
		},
		Capture: CaptureLaunch{
			Params: capturerun.Params{
				Match:       []string{"eng-*"},
				Types:       []string{"running-config"},
				StorePath:   "/home/lab/captures",
				Concurrency: 4,
				Timeout:     30 * time.Second,
				VaultPath:   secretVaultPath,
			},
			Auth:        LaunchAuth{Username: "netops", Password: secretCapPass},
			ManualCreds: true,
		},
		Search: SearchLaunch{
			Query:     "ip route",
			StorePath: "/home/lab/captures",
			Limit:     500,
		},
		QuickConnect: sessions.Node{
			Name:          "eng-leaf-1",
			Transport:     sessions.TransportSSH,
			Host:          "172.16.2.11",
			Port:          22,
			Username:      "netops",
			Password:      secretNodePass,
			KeyPassphrase: secretNodePhr,
			Jump: sessions.JumpSpec{
				Host:          "172.16.0.10",
				Username:      "netops",
				Password:      secretJumpPass,
				KeyPassphrase: secretJumpPhr,
			},
		},
	}
}

// ── the one that matters ─────────────────────────────────────────────

// A byte scan rather than a field check: a secret field added later would be
// marshalled by the encoder without anyone adding a case here, and this is the
// test that notices.
func TestNoSecretReachesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), LaunchesFileName)
	if err := SaveLaunches(path, fullState()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Every distinctive marker, and the marker prefix itself, so a field
	// nobody thought of still trips this.
	for _, secret := range []string{
		secretPass, secretCapPass, secretNodePass, secretNodePhr,
		secretJumpPass, secretJumpPhr, secretVaultPath, "zzz-",
	} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("the launch file contains %q:\n%s", secret, raw)
		}
	}
}

// Not just an empty value — the key itself must not be in the file. A
// "password" field sitting there empty is something a person will fill in.
func TestNoPasswordKeyExistsAtAll(t *testing.T) {
	path := filepath.Join(t.TempDir(), LaunchesFileName)
	if err := SaveLaunches(path, fullState()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"assword", "assphrase", "vault_path"} {
		if strings.Contains(string(raw), key) {
			t.Fatalf("the launch file has a %q key in it:\n%s", key, raw)
		}
	}
}

func TestFilePermissionsAreOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), LaunchesFileName)
	if err := SaveLaunches(path, fullState()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("launch file mode is %o, want 600", perm)
	}
}

// ── round trip ───────────────────────────────────────────────────────

func TestRoundTripKeepsTheUsefulFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), LaunchesFileName)
	if err := SaveLaunches(path, fullState()); err != nil {
		t.Fatal(err)
	}
	back, err := LoadLaunches(path)
	if err != nil {
		t.Fatal(err)
	}

	if got := strings.Join(back.Crawl.Params.Seeds, ","); got != "172.16.2.5,172.16.2.6" {
		t.Fatalf("seeds came back as %q", got)
	}
	if back.Crawl.Params.Depth != 3 || back.Crawl.Params.Concurrency != 8 {
		t.Fatalf("crawl numbers came back as %+v", back.Crawl.Params)
	}
	if back.Crawl.Params.Timeout != 20*time.Second {
		t.Fatalf("crawl timeout came back as %v", back.Crawl.Params.Timeout)
	}
	if !back.Crawl.ManualCreds {
		t.Fatal("the credential-source choice did not survive")
	}
	if back.Crawl.Auth.Username != "netops" {
		t.Fatalf("username came back as %q", back.Crawl.Auth.Username)
	}
	if back.Crawl.Auth.KeyPath == "" {
		t.Fatal("key path did not survive — it is a path, not a secret")
	}
	if !back.Crawl.Params.Legacy {
		t.Fatal("the legacy-SSH choice did not survive, and it is per-estate")
	}
	if back.Capture.Params.StorePath != "/home/lab/captures" {
		t.Fatalf("capture store came back as %q", back.Capture.Params.StorePath)
	}
	if strings.Join(back.Capture.Params.Match, ",") != "eng-*" {
		t.Fatalf("capture match came back as %v", back.Capture.Params.Match)
	}
	if back.Search.Query != "ip route" {
		t.Fatalf("search query came back as %q", back.Search.Query)
	}
	if back.QuickConnect.Host != "172.16.2.11" || back.QuickConnect.Username != "netops" {
		t.Fatalf("quick connect came back as %+v", back.QuickConnect)
	}
	if back.QuickConnect.Jump.Host != "172.16.0.10" {
		t.Fatalf("jump host came back as %q", back.QuickConnect.Jump.Host)
	}
}

func TestSecretsAreAbsentAfterLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), LaunchesFileName)
	if err := SaveLaunches(path, fullState()); err != nil {
		t.Fatal(err)
	}
	back, err := LoadLaunches(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.Crawl.Auth.Password != "" || back.Capture.Auth.Password != "" {
		t.Fatal("a launch password survived the round trip")
	}
	if back.QuickConnect.Password != "" || back.QuickConnect.KeyPassphrase != "" {
		t.Fatal("a node secret survived the round trip")
	}
	if back.QuickConnect.Jump.Password != "" || back.QuickConnect.Jump.KeyPassphrase != "" {
		t.Fatal("a jump secret survived the round trip")
	}
}

// The vault path is recomputed by the host on every launch, and clearing it on
// the manual path is what the credential-source selector exists to do.
// Persisting a stale one would re-introduce that bug.
func TestVaultPathIsNotPersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), LaunchesFileName)
	if err := SaveLaunches(path, fullState()); err != nil {
		t.Fatal(err)
	}
	back, err := LoadLaunches(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.Crawl.Params.VaultPath != "" || back.Capture.Params.VaultPath != "" {
		t.Fatal("a vault path was persisted")
	}
}

// ── failure stances, matching settingsstore ──────────────────────────

func TestMissingFileIsAFirstRun(t *testing.T) {
	got, err := LoadLaunches(filepath.Join(t.TempDir(), "nothing-here.json"))
	if err != nil {
		t.Fatalf("a missing file should not be an error: %v", err)
	}
	if len(got.Crawl.Params.Seeds) != 0 {
		t.Fatal("a missing file produced state")
	}
}

func TestEmptyFileIsAFirstRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), LaunchesFileName)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLaunches(path); err != nil {
		t.Fatalf("an empty file should not be an error: %v", err)
	}
}

// A corrupt file is an error AND usable state: the application still opens.
func TestCorruptFileReportsAndStillOpens(t *testing.T) {
	path := filepath.Join(t.TempDir(), LaunchesFileName)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadLaunches(path)
	if err == nil {
		t.Fatal("a corrupt file should report an error")
	}
	if len(got.Crawl.Params.Seeds) != 0 {
		t.Fatal("a corrupt file produced state")
	}
}

// A file written by an older build, or edited by hand, must not be able to put
// a password back into a form.
func TestSecretsInAHandEditedFileAreIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), LaunchesFileName)
	raw, err := json.Marshal(launchesFile{
		Version: launchesFileVersion,
		State: LaunchState{
			Crawl: CrawlLaunch{Auth: LaunchAuth{Username: "netops", Password: secretPass}},
			QuickConnect: sessions.Node{
				Host:     "172.16.2.11",
				Password: secretNodePass,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	back, err := LoadLaunches(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.Crawl.Auth.Password != "" || back.QuickConnect.Password != "" {
		t.Fatal("a hand-written password was loaded into the launch state")
	}
	if back.Crawl.Auth.Username != "netops" {
		t.Fatal("redaction took the username too")
	}
}

// ── the in-memory half ───────────────────────────────────────────────

// RedactNode is what the host calls before keeping a quick-connect node, so
// the password is not offered back on the next open.
func TestRedactNodeClearsBothLevels(t *testing.T) {
	n := RedactNode(fullState().QuickConnect)
	if n.Password != "" || n.KeyPassphrase != "" {
		t.Fatal("node secrets survived RedactNode")
	}
	if n.Jump.Password != "" || n.Jump.KeyPassphrase != "" {
		t.Fatal("jump secrets survived RedactNode")
	}
	if n.Host != "172.16.2.11" || n.Jump.Host != "172.16.0.10" {
		t.Fatal("RedactNode cleared more than the secrets")
	}
}
