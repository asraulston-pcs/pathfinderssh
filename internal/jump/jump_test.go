// internal/jump/jump_test.go
package jump

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleYAML = `
jump_hosts:
  lab-bastion:
    host: 192.0.2.10
    port: 2222
    credential: bastion-lab
  dmz-jump:
    host: 198.51.100.5
    credential: bastion-dmz
    via: lab-bastion

proxy_rules:
  - match: {devices: [lab-bastion]}
    jump: direct
  - match: {devices: ["fw*-dmz"]}
    jump: dmz-jump
  - match: {cidrs: ["10.20.0.0/16"]}
    jump: lab-bastion
  - match: {platform: [juniper_junos]}
    jump: lab-bastion
  - match: {}
    jump: inherit
`

func mustResolver(t *testing.T, yamlBody string) *Resolver {
	t.Helper()
	cfg, err := ParseConfig([]byte(yamlBody))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	r, err := NewResolver(cfg, nil)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	return r
}

func TestFirstMatchWins(t *testing.T) {
	r := mustResolver(t, sampleYAML)

	// The device glob sits above the CIDR rule, so it wins even though the
	// address would also match the broader rule.
	dec := r.Resolve(Device{Name: "fw1-dmz", Addr: "10.20.4.9"})
	if dec.RuleIndex != 1 {
		t.Fatalf("RuleIndex = %d, want 1", dec.RuleIndex)
	}
	if dec.Path.String() != "lab-bastion -> dmz-jump" {
		t.Fatalf("path = %q, want the chained path", dec.Path.String())
	}
}

func TestMatchIsAnd(t *testing.T) {
	r := mustResolver(t, `
jump_hosts:
  b1: {host: 192.0.2.10, credential: c1}
proxy_rules:
  - match: {devices: ["spine*"], platform: [arista_eos]}
    jump: b1
  - match: {}
    jump: direct
`)
	// Name matches, platform does not: the rule must not fire.
	if dec := r.Resolve(Device{Name: "spine1", Platform: "juniper_junos"}); dec.RuleIndex != 1 {
		t.Fatalf("partial match fired rule %d", dec.RuleIndex)
	}
	if dec := r.Resolve(Device{Name: "spine1", Platform: "arista_eos"}); dec.RuleIndex != 0 {
		t.Fatalf("full match did not fire, got rule %d", dec.RuleIndex)
	}
}

func TestPlatformRuleSkippedWhenUnknown(t *testing.T) {
	r := mustResolver(t, sampleYAML)
	// No platform yet (pre-fingerprint, no neighbor claim): the platform rule
	// must not match, so this falls to the catch-all.
	dec := r.Resolve(Device{Name: "qfx1", Addr: "203.0.113.7"})
	if dec.RuleIndex != 4 {
		t.Fatalf("RuleIndex = %d, want the catch-all at 4", dec.RuleIndex)
	}
	// With the neighbor's claim populated, the platform rule fires.
	dec = r.Resolve(Device{Name: "qfx1", Addr: "203.0.113.7", Platform: "juniper_junos"})
	if dec.RuleIndex != 3 || dec.Path.String() != "lab-bastion" {
		t.Fatalf("RuleIndex = %d, path = %q", dec.RuleIndex, dec.Path.String())
	}
}

func TestCIDRMatch(t *testing.T) {
	r := mustResolver(t, sampleYAML)
	if dec := r.Resolve(Device{Name: "spine1", Addr: "10.20.4.9"}); dec.RuleIndex != 2 {
		t.Fatalf("in-range RuleIndex = %d, want 2", dec.RuleIndex)
	}
	if dec := r.Resolve(Device{Name: "spine1", Addr: "10.99.4.9"}); dec.RuleIndex != 4 {
		t.Fatalf("out-of-range RuleIndex = %d, want the catch-all", dec.RuleIndex)
	}
	// No address at all cannot match a CIDR rule.
	if dec := r.Resolve(Device{Name: "spine1"}); dec.RuleIndex != 4 {
		t.Fatalf("address-less RuleIndex = %d, want the catch-all", dec.RuleIndex)
	}
}

func TestViaChainOrderAndPort(t *testing.T) {
	r := mustResolver(t, sampleYAML)
	dec := r.Resolve(Device{Name: "fw9-dmz"})
	if len(dec.Path) != 2 {
		t.Fatalf("len(path) = %d, want 2", len(dec.Path))
	}
	// Outermost bastion is dialed first.
	if dec.Path[0].Name != "lab-bastion" || dec.Path[1].Name != "dmz-jump" {
		t.Fatalf("chain order = %v", dec.Path.String())
	}
	if dec.Path[0].Port != 2222 {
		t.Fatalf("explicit port lost: %d", dec.Path[0].Port)
	}
	if dec.Path[1].Port != 22 {
		t.Fatalf("default port = %d, want 22", dec.Path[1].Port)
	}
	if dec.Path[0].Credential != "bastion-lab" {
		t.Fatalf("credential name lost: %q", dec.Path[0].Credential)
	}
}

func TestInheritUsesNeighborPath(t *testing.T) {
	r := mustResolver(t, sampleYAML)
	inherited := Path{{Name: "lab-bastion", Host: "192.0.2.10", Port: 2222, Credential: "bastion-lab"}}

	dec := r.Resolve(Device{Name: "leaf7", Addr: "203.0.113.20", InheritedPath: inherited})
	if !dec.Inherited {
		t.Fatal("expected the decision to be marked inherited")
	}
	if dec.Path.String() != "lab-bastion" {
		t.Fatalf("inherited path = %q", dec.Path.String())
	}
}

func TestInheritFallsBackToDirect(t *testing.T) {
	r := mustResolver(t, sampleYAML)
	dec := r.Resolve(Device{Name: "leaf7", Addr: "203.0.113.20"})
	if !dec.Path.IsDirect() {
		t.Fatalf("want direct with no inherited path, got %q", dec.Path.String())
	}
	if dec.Inherited {
		t.Fatal("nothing was inherited")
	}
}

func TestInheritDoesNotMutateSource(t *testing.T) {
	r := mustResolver(t, sampleYAML)
	inherited := Path{
		{Name: "lab-bastion", Host: "192.0.2.10", Credential: "bastion-lab"},
		{Name: "dmz-jump", Host: "198.51.100.5", Credential: "bastion-dmz"},
	}
	// Resolving a device that IS one of the inherited hops strips it; the
	// caller's slice must be untouched.
	_ = r.Resolve(Device{Name: "dmz-jump", InheritedPath: inherited})
	if len(inherited) != 2 {
		t.Fatalf("source path mutated: %v", inherited.String())
	}
}

func TestBastionIsNeverReachedThroughItself(t *testing.T) {
	// No explicit direct rule for the bastion here: the structural backstop
	// has to catch it.
	r := mustResolver(t, `
jump_hosts:
  lab-bastion: {host: 192.0.2.10, credential: bastion-lab}
proxy_rules:
  - match: {}
    jump: lab-bastion
`)
	if dec := r.Resolve(Device{Name: "lab-bastion"}); !dec.Path.IsDirect() {
		t.Fatalf("bastion routed through itself by name: %q", dec.Path.String())
	}
	if dec := r.Resolve(Device{Name: "someothername", Addr: "192.0.2.10"}); !dec.Path.IsDirect() {
		t.Fatalf("bastion routed through itself by address: %q", dec.Path.String())
	}
}

func TestNoRuleMatchesIsDirect(t *testing.T) {
	r := mustResolver(t, `
jump_hosts:
  b1: {host: 192.0.2.10, credential: c1}
proxy_rules:
  - match: {devices: ["fw*"]}
    jump: b1
`)
	dec := r.Resolve(Device{Name: "spine1"})
	if !dec.Path.IsDirect() || dec.RuleIndex != -1 {
		t.Fatalf("unmatched device = %q rule %d, want direct/-1", dec.Path.String(), dec.RuleIndex)
	}
}

// --- validation ---

func TestValidationRejectsBadConfigs(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{"undefined jump host", `
jump_hosts:
  b1: {host: 192.0.2.10, credential: c1}
proxy_rules:
  - match: {}
    jump: nope
`, "undefined jump host"},
		{"missing credential", `
jump_hosts:
  b1: {host: 192.0.2.10}
proxy_rules:
  - match: {}
    jump: b1
`, "no 'credential'"},
		{"missing host address", `
jump_hosts:
  b1: {credential: c1}
proxy_rules:
  - match: {}
    jump: b1
`, "no 'host'"},
		{"missing jump target", `
jump_hosts:
  b1: {host: 192.0.2.10, credential: c1}
proxy_rules:
  - match: {}
`, "no 'jump' target"},
		{"bad regex", `
jump_hosts:
  b1: {host: 192.0.2.10, credential: c1}
proxy_rules:
  - match: {name_regex: "spine(["}
    jump: b1
`, "invalid name_regex"},
		{"bad cidr", `
jump_hosts:
  b1: {host: 192.0.2.10, credential: c1}
proxy_rules:
  - match: {cidrs: ["10.20.0.0/99"]}
    jump: b1
`, "invalid cidr"},
		{"cyclic via", `
jump_hosts:
  b1: {host: 192.0.2.10, credential: c1, via: b2}
  b2: {host: 192.0.2.11, credential: c2, via: b1}
proxy_rules:
  - match: {}
    jump: b1
`, "cyclic"},
		{"reserved name", `
jump_hosts:
  direct: {host: 192.0.2.10, credential: c1}
proxy_rules:
  - match: {}
    jump: direct
`, "reserved"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParseConfig([]byte(tc.yaml))
			if err == nil {
				_, err = NewResolver(cfg, nil)
			}
			if err == nil {
				t.Fatal("expected an error, got none")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestUnknownKeysRejected(t *testing.T) {
	_, err := ParseConfig([]byte(`
jump_hosts:
  b1: {host: 192.0.2.10, credential: c1}
proxy_rules:
  - match: {device: ["spine1"]}
    jump: b1
`))
	if err == nil {
		t.Fatal("a typo'd match key must not silently widen the rule")
	}
}

func TestCatchAllAboveOtherRulesIsFlagged(t *testing.T) {
	var notes []string
	cfg, err := ParseConfig([]byte(`
jump_hosts:
  b1: {host: 192.0.2.10, credential: c1}
proxy_rules:
  - match: {}
    jump: b1
  - match: {devices: ["fw*"]}
    jump: direct
`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if _, err := NewResolver(cfg, func(f string, a ...any) {
		notes = append(notes, f)
	}); err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	if len(notes) == 0 {
		t.Fatal("expected a warning about unreachable rules")
	}
}

// --- config file handling ---

func TestLoadMissingFileIsNotAnError(t *testing.T) {
	r, ok, err := Load(filepath.Join(t.TempDir(), "absent.yaml"), nil)
	if err != nil {
		t.Fatalf("missing config should be fine: %v", err)
	}
	if ok || r != nil {
		t.Fatal("missing config should yield no resolver")
	}
}

func TestLoadMalformedFileIsAnError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "jump_hosts.yaml")
	if err := os.WriteFile(p, []byte("jump_hosts: [this is not a mapping"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, _, err := Load(p, nil); err == nil {
		t.Fatal("malformed config must not silently fall back to direct")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cfg", DefaultConfigName)
	cfg, err := ParseConfig([]byte(sampleYAML))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if err := Save(p, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("config mode = %o, want 0600", perm)
	}

	r, ok, err := Load(p, nil)
	if err != nil || !ok {
		t.Fatalf("Load after Save: ok=%v err=%v", ok, err)
	}
	dec := r.Resolve(Device{Name: "fw1-dmz"})
	if dec.Path.String() != "lab-bastion -> dmz-jump" {
		t.Fatalf("round-trip changed routing: %q", dec.Path.String())
	}
}

// --- credential binding ---

func TestBindResolvesNames(t *testing.T) {
	r := mustResolver(t, sampleYAML)
	dec := r.Resolve(Device{Name: "fw1-dmz"})

	calls := 0
	lookup := CachedLookup(func(name string) (Credential, error) {
		calls++
		return Credential{Username: "netops", Password: "x-" + name}, nil
	})

	bound, err := Bind(dec.Path, lookup)
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if len(bound) != 2 {
		t.Fatalf("len = %d, want 2", len(bound))
	}
	if bound[0].Credential.Password != "x-bastion-lab" {
		t.Fatalf("wrong credential bound: %+v", bound[0].Hop)
	}

	// A second bind of the same path must hit the memo, not the lookup.
	if _, err := Bind(dec.Path, lookup); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if calls != 2 {
		t.Fatalf("lookup called %d times, want 2 (one per distinct bastion)", calls)
	}
}

func TestBindMissingCredentialIsFatal(t *testing.T) {
	r := mustResolver(t, sampleYAML)
	dec := r.Resolve(Device{Name: "fw1-dmz"})

	_, err := Bind(dec.Path, func(string) (Credential, error) {
		return Credential{}, errors.New("not found")
	})
	if err == nil {
		t.Fatal("an unresolvable jump credential must not fall back to direct")
	}
	if !strings.Contains(err.Error(), "jump config") {
		t.Fatalf("error should say how to fix it, got %q", err)
	}
}

func TestBindDirectPathIsEmpty(t *testing.T) {
	bound, err := Bind(nil, nil)
	if err != nil || !bound.IsDirect() {
		t.Fatalf("direct path should bind trivially: %v %v", bound, err)
	}
}
