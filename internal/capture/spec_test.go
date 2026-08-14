// internal/capture/spec_test.go
//
// The read-only guarantee, enforced at build time.
//
// This is the test ROADMAP asks for, and the reason the specs are Go values:
// a command cannot reach a device unless it is written into a spec here, and a
// spec cannot compile green unless the command is also in the allowlist below.
// Adding a command is therefore a two-place edit, and the second place is a
// list whose only purpose is to be read by whoever reviews it.
package capture

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// readOnly is the set of commands this product is permitted to send.
//
// It is an EXACT-STRING list, not a pattern. A verb-prefix rule was the
// obvious first design and it is wrong in both directions: "show" would admit
// nothing dangerous but also reject Junos's "request support information",
// which is read-only, while "request" as a permitted verb would admit
// "request system reboot". There is no prefix that separates those. A list
// separates them.
//
// Nothing is added here without someone having confirmed on real gear that it
// neither changes state nor prompts for confirmation.
var readOnly = map[string]bool{
	// Configuration.
	"show running-config":              true,
	"show startup-config":              true,
	"show configuration | display set": true,
	// Inventory.
	"show inventory":        true,
	"show chassis hardware": true,
}

func TestEveryBuiltinCommandIsOnTheReadOnlyAllowlist(t *testing.T) {
	for _, spec := range Builtin() {
		for platform, cmd := range spec.Commands {
			if !readOnly[cmd.Command] {
				t.Errorf("%s/%s sends %q, which is not on the read-only allowlist. "+
					"If it is genuinely read-only, add it there deliberately; if you are not sure, it does not ship.",
					spec.Type, platform, cmd.Command)
			}
		}
	}
}

// The allowlist is only a guarantee if it stays a list of things actually in
// use. An entry with no spec behind it is a permission granted to nobody,
// which is how a list stops being read.
func TestTheAllowlistHasNoUnusedEntries(t *testing.T) {
	used := map[string]bool{}
	for _, spec := range Builtin() {
		for _, cmd := range spec.Commands {
			used[cmd.Command] = true
		}
	}
	for cmd := range readOnly {
		if !used[cmd] {
			t.Errorf("%q is allowed but no spec sends it; remove it", cmd)
		}
	}
}

// A second, independent check on the same commands. The allowlist protects
// against a dangerous command being added; this protects against the
// allowlist itself being edited carelessly, since these words have no
// business in anything this product sends.
//
// Matched on word boundaries rather than as substrings. The first version of
// this test used strings.Contains and failed on Junos's "request support
// information", because "information" contains "format" — a crude matcher
// does not produce a stricter test, it produces a test that gets edited
// until it is quiet.
func TestNoBuiltinCommandLooksLikeAWrite(t *testing.T) {
	forbidden := []string{
		`conf`, `configure`, `write`, `copy`, `delete`, `erase`,
		`reload`, `reboot`, `install`, `commit`, `format`, `shutdown`,
		`clear`, `no`,
	}
	var pats []*regexp.Regexp
	for _, w := range forbidden {
		pats = append(pats, regexp.MustCompile(`\b`+regexp.QuoteMeta(w)+`\b`))
	}
	// "set" is deliberately absent from the word list: Junos uses it as a
	// read-only output modifier ("| display set") and as the configuration
	// verb, and a word-boundary match cannot tell them apart. The exact
	// allowlist above is what covers that command.
	for _, spec := range Builtin() {
		for platform, cmd := range spec.Commands {
			lower := strings.ToLower(cmd.Command)
			for _, re := range pats {
				if re.MatchString(lower) {
					t.Errorf("%s/%s: %q contains %q", spec.Type, platform, cmd.Command, re.String())
				}
			}
		}
	}
}

func TestEveryBuiltinSpecValidates(t *testing.T) {
	for _, spec := range Builtin() {
		if err := spec.Validate(); err != nil {
			t.Errorf("%s: %v", spec.Type, err)
		}
	}
}

func TestBuiltinTypesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, spec := range Builtin() {
		if seen[spec.Type] {
			t.Errorf("duplicate capture type %q — the second would share the first's directory", spec.Type)
		}
		seen[spec.Type] = true
	}
}

// A platform absent from a spec is "not applicable", and the engine has to be
// able to tell that apart from a failure. Junos is the live example: there is
// no startup/running split, so there is nothing to capture and nothing wrong.
func TestAMissingPlatformIsNotApplicableRatherThanAnError(t *testing.T) {
	if _, ok := StartupConfig.For("juniper_junos"); ok {
		t.Error("junos has no startup-config; a command here would store the running config twice under two names")
	}
	if _, ok := RunningConfig.For("juniper_junos"); !ok {
		t.Error("junos should have a running-config command")
	}
	if _, ok := RunningConfig.For("linux"); ok {
		t.Error("linux has no running-config")
	}
}

// No builtin is expensive any more. tech-support was the only one and it was
// removed: shipping it put a full diagnostic bundle off every device in an
// estate one click away. This test is what stops it, or anything like it,
// coming back without the decision being made again.
func TestNoBuiltinRunsInTheExpensiveLane(t *testing.T) {
	for _, spec := range Builtin() {
		for platform, cmd := range spec.Commands {
			if cmd.Cost != CostCheap {
				t.Errorf("%s/%s is in the expensive lane. An expensive builtin is a "+
					"fleet-wide diagnostic collection that a person can start by "+
					"ticking a box; if it genuinely belongs, say so here deliberately.",
					spec.Type, platform)
			}
		}
	}
}

// The lane still has to work, because the next expensive command will need it
// and it should not be rebuilt during an incident. Pinned against a spec
// declared here rather than a shipped one, so the rule survives having no
// builtin behind it.
func TestAnExpensiveCommandCarriesItsOwnBoundsAndLane(t *testing.T) {
	diagnostic := Spec{
		Type:        "lab-diagnostic",
		Description: "not shipped; exists to pin the expensive-lane rules",
		Commands: map[string]Command{
			"cisco_ios": {
				Command:  "show lab diagnostic",
				Cost:     CostExpensive,
				MaxBytes: 64 << 20,
				Timeout:  15 * time.Minute,
			},
		},
	}
	for platform, cmd := range diagnostic.Commands {
		if cmd.Cost != CostExpensive {
			t.Errorf("%s is not marked expensive", platform)
		}
		if cmd.MaxBytes == 0 {
			t.Errorf("%s takes the default output limit; it will be refused as too large", platform)
		}
		if cmd.Timeout == 0 {
			t.Errorf("%s takes the default command timeout", platform)
		}
	}
	if err := diagnostic.Validate(); err != nil {
		t.Errorf("an expensive spec no longer validates: %v", err)
	}
}

func TestRoutineBackupsStayInTheCheapLane(t *testing.T) {
	for platform, cmd := range RunningConfig.Commands {
		if cmd.Cost != CostCheap {
			t.Errorf("running-config/%s is in the expensive lane; a routine backup would serialize", platform)
		}
	}
}

func TestValidateRejectsUnsafeTypes(t *testing.T) {
	bad := []string{"", "../etc", "Running-Config", "running config", "run/cfg", ".hidden"}
	for _, typ := range bad {
		s := Spec{Type: typ, Commands: map[string]Command{"cisco_ios": {Command: "show version"}}}
		if err := s.Validate(); err == nil {
			t.Errorf("Validate accepted type %q as a directory name", typ)
		}
	}
}

func TestValidateRejectsMultiCommandStrings(t *testing.T) {
	s := Spec{Type: "bad", Commands: map[string]Command{
		"cisco_ios": {Command: "show version\nshow inventory"},
	}}
	if err := s.Validate(); err == nil {
		t.Error("a command containing a line break was accepted; netexec.Run refuses these at the wire")
	}
}

func TestLookup(t *testing.T) {
	if s, ok := Lookup("running-config"); !ok || s.Type != "running-config" {
		t.Errorf("Lookup(running-config) = %v, %v", s.Type, ok)
	}
	if _, ok := Lookup("nope"); ok {
		t.Error("Lookup found a type that does not exist")
	}
}
