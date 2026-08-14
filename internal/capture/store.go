// internal/capture/store.go
//
// Where captures land on disk.
//
// Layout, device-major:
//
//	<root>/devices/<slug>/device.json
//	<root>/devices/<slug>/<capture-type>/2026-07-31T15-04-22Z.txt
//	<root>/devices/<slug>/<capture-type>/history.jsonl
//
// Device-major because the dominant question is one device across time, and
// under this shape that question is a directory listing. The cost is that
// "every device's config on Tuesday" becomes a walk — the rarer query, and a
// walk over a few hundred directories is nothing.
//
// # Why identical captures do not get a file
//
// A config that has not changed in six months should not be six months of
// identical files. So a capture whose content hash matches the previous one
// writes no new file.
//
// That dedup is only safe because history.jsonl records the attempt anyway.
// Without it, "captured and identical" and "never captured" look exactly the
// same on disk, and the storage saving would have been paid for with a hole in
// the record — which is the worse trade for a backup.
//
// # Why the directory is a slug and device.json holds the truth
//
// The canonical name comes from a device prompt or a neighbor's claim. Nothing
// guarantees it is a legal path element on three operating systems. So the
// directory is a sanitized slug and the real name lives in device.json, which
// also means a slug collision between two genuinely different devices is
// detectable rather than a silent merge of two config histories.
package capture

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// TimeLayout is the capture filename timestamp: RFC3339 in UTC with the
// colons replaced. Colons are illegal in Windows filenames, and this ships to
// the Microsoft Store.
const TimeLayout = "2006-01-02T15-04-05Z"

// Artifact is the result of storing one capture.
type Artifact struct {
	// Device is the canonical device name, as stored.
	Device string
	// Type is the capture type.
	Type string
	// Path is the file on disk. On an unchanged capture this names the
	// EXISTING file whose content matched, not a new one.
	Path string
	// SHA256 is the hex digest of the stored content.
	SHA256 string
	// Bytes is the content length.
	Bytes int
	// At is the capture time.
	At time.Time
	// Unchanged reports that the content matched the previous capture and
	// no new file was written.
	Unchanged bool
}

// DeviceInfo is what device.json holds — enough to prove which device a
// directory belongs to, and nothing that belongs in a capture file.
type DeviceInfo struct {
	Canonical string    `json:"canonical"`
	Aliases   []string  `json:"aliases,omitempty"`
	Platform  string    `json:"platform,omitempty"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// HistoryEntry is one line of history.jsonl. Every attempt appends one,
// including the ones that wrote no file.
type HistoryEntry struct {
	At        time.Time `json:"at"`
	Command   string    `json:"command"`
	SHA256    string    `json:"sha256"`
	Bytes     int       `json:"bytes"`
	File      string    `json:"file"`
	Unchanged bool      `json:"unchanged,omitempty"`
}

// Store is what the engine writes through. An interface because the engine
// should be testable without a filesystem, and because a future remote or
// encrypted store is a different implementation and not a different engine.
type Store interface {
	// Put stores one capture. Implementations must be safe for concurrent
	// use across different devices.
	Put(dev DeviceInfo, typ, command string, at time.Time, content []byte) (Artifact, error)
	// History returns the recorded attempts for a device and type, oldest
	// first.
	History(canonical, typ string) ([]HistoryEntry, error)
}

// FileStore is the on-disk Store.
type FileStore struct {
	root string

	mu    sync.Mutex
	locks map[string]*sync.Mutex // per-slug, so two devices never serialize
}

// OpenFileStore prepares root for use. The directory is created if missing.
func OpenFileStore(root string) (*FileStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("capture: store root is empty")
	}
	if err := os.MkdirAll(filepath.Join(root, "devices"), 0o700); err != nil {
		return nil, fmt.Errorf("capture: create store root: %w", err)
	}
	return &FileStore{root: root, locks: map[string]*sync.Mutex{}}, nil
}

// Root reports the store's base directory.
func (s *FileStore) Root() string { return s.root }

func (s *FileStore) lockFor(slug string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.locks[slug]
	if !ok {
		m = &sync.Mutex{}
		s.locks[slug] = m
	}
	return m
}

var unsafeRune = regexp.MustCompile(`[^a-z0-9._-]+`)

// Slug turns a canonical device name into a path element.
//
// Lowercased because two of the three target filesystems are
// case-insensitive, and a store that behaves differently on macOS than on
// Linux is a support burden nobody is paid enough for.
func Slug(canonical string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(canonical))
	s = unsafeRune.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-.")
	if s == "" {
		return "", fmt.Errorf("capture: %q has no usable characters for a directory name", canonical)
	}
	if len(s) > 96 {
		s = strings.Trim(s[:96], "-.")
	}
	// Reserved on Windows regardless of extension.
	switch s {
	case "con", "prn", "aux", "nul", "com1", "com2", "com3", "com4", "lpt1", "lpt2", "lpt3":
		s = s + "-dev"
	}
	return s, nil
}

// Put stores one capture, writing a file only when the content is new.
func (s *FileStore) Put(dev DeviceInfo, typ, command string, at time.Time, content []byte) (Artifact, error) {
	if strings.TrimSpace(typ) == "" {
		return Artifact{}, fmt.Errorf("capture: empty capture type")
	}
	slug, err := Slug(dev.Canonical)
	if err != nil {
		return Artifact{}, err
	}
	at = at.UTC()

	lk := s.lockFor(slug)
	lk.Lock()
	defer lk.Unlock()

	devDir := filepath.Join(s.root, "devices", slug)
	typeDir := filepath.Join(devDir, typ)
	if err := os.MkdirAll(typeDir, 0o700); err != nil {
		return Artifact{}, fmt.Errorf("capture: create %s: %w", typeDir, err)
	}
	if err := s.mergeDeviceInfo(devDir, dev, at); err != nil {
		return Artifact{}, err
	}

	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])

	prev, err := s.lastStored(typeDir)
	if err != nil {
		return Artifact{}, err
	}

	art := Artifact{
		Device: dev.Canonical,
		Type:   typ,
		SHA256: digest,
		Bytes:  len(content),
		At:     at,
	}

	if prev != nil && prev.SHA256 == digest {
		art.Unchanged = true
		art.Path = filepath.Join(typeDir, prev.File)
		entry := HistoryEntry{At: at, Command: command, SHA256: digest,
			Bytes: len(content), File: prev.File, Unchanged: true}
		if err := appendHistory(typeDir, entry); err != nil {
			return Artifact{}, err
		}
		return art, nil
	}

	name := at.Format(TimeLayout) + ".txt"
	path := filepath.Join(typeDir, name)
	// A same-second second capture would otherwise overwrite the first.
	for i := 1; fileExists(path); i++ {
		name = fmt.Sprintf("%s-%d.txt", at.Format(TimeLayout), i)
		path = filepath.Join(typeDir, name)
	}
	if err := writeFileAtomic(path, content); err != nil {
		return Artifact{}, err
	}
	art.Path = path
	entry := HistoryEntry{At: at, Command: command, SHA256: digest,
		Bytes: len(content), File: name}
	if err := appendHistory(typeDir, entry); err != nil {
		return Artifact{}, err
	}
	return art, nil
}

// History returns every recorded attempt, oldest first. A device or type that
// has never been captured is not an error — it returns nothing.
func (s *FileStore) History(canonical, typ string) ([]HistoryEntry, error) {
	dir, err := s.deviceDir(canonical)
	if err != nil {
		return nil, err
	}
	t, err := element("capture type", typ)
	if err != nil {
		return nil, err
	}
	return readHistory(filepath.Join(dir, t))
}

// mergeDeviceInfo writes or updates device.json.
//
// A slug that already belongs to a different canonical name is refused rather
// than merged. Two devices sharing one config history is the failure this
// whole naming scheme exists to avoid, and it is much easier to notice as an
// error at capture time than as a diff that makes no sense in six months.
func (s *FileStore) mergeDeviceInfo(devDir string, dev DeviceInfo, at time.Time) error {
	if err := os.MkdirAll(devDir, 0o700); err != nil {
		return err
	}
	existing, err := readDeviceInfo(devDir)
	if err != nil {
		return err
	}
	out := DeviceInfo{
		Canonical: dev.Canonical,
		Aliases:   append([]string(nil), dev.Aliases...),
		Platform:  dev.Platform,
		FirstSeen: at,
		LastSeen:  at,
	}
	if existing != nil {
		if !strings.EqualFold(existing.Canonical, dev.Canonical) {
			return fmt.Errorf(
				"capture: directory %s already holds %q; %q would share its history (rename one or capture to a different root)",
				filepath.Base(devDir), existing.Canonical, dev.Canonical)
		}
		out.Canonical = existing.Canonical
		out.FirstSeen = existing.FirstSeen
		out.Aliases = mergeStrings(existing.Aliases, dev.Aliases)
		if out.Platform == "" {
			out.Platform = existing.Platform
		}
	}
	buf, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(devDir, "device.json"), append(buf, '\n'))
}

func readDeviceInfo(devDir string) (*DeviceInfo, error) {
	buf, err := os.ReadFile(filepath.Join(devDir, "device.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var d DeviceInfo
	if err := json.Unmarshal(buf, &d); err != nil {
		return nil, fmt.Errorf("capture: %s/device.json: %w", devDir, err)
	}
	return &d, nil
}

// lastStored returns the most recent history entry that actually has a file
// behind it. Unchanged entries carry the earlier file's name, so this works
// whether or not the previous attempt wrote anything.
func (s *FileStore) lastStored(typeDir string) (*HistoryEntry, error) {
	entries, err := readHistory(typeDir)
	if err != nil {
		return nil, err
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].File != "" {
			e := entries[i]
			return &e, nil
		}
	}
	return nil, nil
}

func readHistory(typeDir string) ([]HistoryEntry, error) {
	buf, err := os.ReadFile(filepath.Join(typeDir, "history.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []HistoryEntry
	for n, line := range strings.Split(string(buf), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e HistoryEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("capture: %s/history.jsonl line %d: %w", typeDir, n+1, err)
		}
		out = append(out, e)
	}
	return out, nil
}

func appendHistory(typeDir string, e HistoryEntry) error {
	buf, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(typeDir, "history.jsonl"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(buf, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

// writeFileAtomic writes through a temp file in the same directory, so a
// crash mid-write leaves the previous capture intact rather than a half
// config that still parses.
func writeFileAtomic(path string, content []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func mergeStrings(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range append(append([]string(nil), a...), b...) {
		v = strings.TrimSpace(v)
		if v == "" || seen[strings.ToLower(v)] {
			continue
		}
		seen[strings.ToLower(v)] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
