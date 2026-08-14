// internal/jump/config.go
//
// YAML loading for the jump-host config.
//
// The file shape is the one already in use in secure-cartography
// (~/.scng/jump_hosts.yaml), so an existing config is portable:
//
//	jump_hosts:
//	  lab-bastion:
//	    host: 192.0.2.10
//	    port: 22
//	    credential: bastion-lab       # name of a vault credential
//	  dmz-jump:
//	    host: 198.51.100.5
//	    credential: bastion-dmz
//	    via: lab-bastion              # optional: chained, multi-hop
//
//	proxy_rules:
//	  - match: {devices: [lab-bastion]}    # a bastion is never reached via itself
//	    jump: direct
//	  - match: {devices: ["fw*-dmz"]}      # exception: must sit above the broad rule
//	    jump: dmz-jump
//	  - match: {cidrs: ["10.20.0.0/16"]}
//	    jump: lab-bastion
//	  - match: {platform: [juniper_junos]}
//	    jump: lab-bastion
//	  - match: {}                          # catch-all
//	    jump: inherit
//
// Rules are evaluated top to bottom, FIRST MATCH WINS, and every key present
// in one rule must match (AND). That is route-map ordering, not implicit
// specificity: an exception is expressed by position, so the narrow rule goes
// above the broad one.
//
// A missing file is not an error - it means "no jump hosts configured" and
// every device is dialed directly. A malformed file IS an error, because
// silently falling back to direct connections looks like a working crawl that
// quietly skipped every bastion-only device.
package jump

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// DefaultConfigName is the conventional file name inside the app config
// directory.
const DefaultConfigName = "jump_hosts.yaml"

// DefaultConfigPath returns the conventional config location for the current
// user, or an empty string if the home directory cannot be determined.
func DefaultConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "pathfinder", DefaultConfigName)
}

// LoadConfig reads and parses a jump config. A missing file returns a zero
// Config and ok=false, with no error.
func LoadConfig(path string) (cfg Config, ok bool, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, false, nil
		}
		return Config{}, false, fmt.Errorf("jump: failed to read %s: %w", path, err)
	}
	cfg, err = ParseConfig(raw)
	if err != nil {
		return Config{}, false, fmt.Errorf("jump: %s: %w", path, err)
	}
	if len(cfg.Hosts) == 0 && len(cfg.Rules) == 0 {
		return Config{}, false, nil
	}
	return cfg, true, nil
}

// ParseConfig parses the YAML body. Unknown keys are rejected so a typo in a
// match key does not silently widen a rule.
func ParseConfig(raw []byte) (Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("invalid YAML: %w", err)
	}
	for name, h := range cfg.Hosts {
		h.Name = name
		cfg.Hosts[name] = h
	}
	return cfg, nil
}

// Load reads a config from path and builds a validated resolver in one step.
// A missing file yields a nil resolver and ok=false, which callers should
// treat as "everything is direct".
func Load(path string, logf func(format string, args ...any)) (*Resolver, bool, error) {
	cfg, ok, err := LoadConfig(path)
	if err != nil || !ok {
		return nil, false, err
	}
	r, err := NewResolver(cfg, logf)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", path, err)
	}
	return r, true, nil
}

// Save writes a config back to path atomically, 0600. The manager UI uses this
// so a Store user never has to find and hand-edit the file, while the file
// stays plain YAML for the people who prefer to.
func Save(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("jump: failed to create config directory: %w", err)
	}
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("jump: failed to marshal config: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0600); err != nil {
		return fmt.Errorf("jump: failed to write config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("jump: failed to commit config: %w", err)
	}
	return nil
}
