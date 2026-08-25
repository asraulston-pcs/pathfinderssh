// internal/netexec/fingerprint_test.go
// Unit tests for the classification half of fingerprinting (the probe loop
// itself needs a live session — lab work).
package netexec

import "testing"

func TestIsCLIError(t *testing.T) {
	errors := []string{
		"% Invalid input detected at '^' marker.",
		"                 ^\n% Invalid input detected",
		"syntax error, expecting <command>.",
		"ERROR: % Invalid input detected",
		"% Unrecognized command found at '^' position.",
		"Error: Ambiguous command",
		"bad command name set (line 1)",
		"bash: frobnicate: command not found",
	}
	for _, e := range errors {
		if !isCLIError(e) {
			t.Errorf("isCLIError(%q) = false, want true", e)
		}
	}
	notErrors := []string{
		"", // silent success
		"Arista DCS-7280SRA-48C6\nSoftware image version: 4.33.1.1F",
		"Cisco IOS Software, C2960 Software",
		"Screen length is set to 0", // some stacks confirm politely
	}
	for _, ok := range notErrors {
		if isCLIError(ok) {
			t.Errorf("isCLIError(%q) = true, want false", ok)
		}
	}
}

// classesFor returns the classes list of whichever probe classifies to the
// given platform name, rather than requiring the caller to know that
// probe's position in the probes slice.
//
// TestClassifyVersions used to index probes by position (probes[0],
// probes[1], ...), which meant inserting or reordering a probe anywhere
// in the table silently shifted every later test case onto the wrong
// probe -- a real problem hit twice over the course of adding aruba_cx
// and extreme_exos support, each insertion requiring every subsequent
// case to be manually renumbered. Looking a probe up by a platform it is
// known to classify is stable under reordering: as long as that
// classification entry still exists somewhere in probes, the test finds
// the right one regardless of where it lives in the list.
func classesFor(t *testing.T, platform string) []versionClass {
	t.Helper()
	for _, p := range probes {
		for _, c := range p.classes {
			if c.name == platform {
				return p.classes
			}
		}
	}
	t.Fatalf("no probe in probes classifies to platform %q", platform)
	return nil
}

func TestClassifyVersions(t *testing.T) {
	cases := []struct {
		// viaPlatform names a platform this probe's classes list is known
		// to classify, used only to locate the right probe -- it need not
		// equal want, e.g. a probe that also classifies "some future
		// platform" text as no match at all.
		viaPlatform string
		out         string
		want        string
	}{
		{"arista_eos", "Arista DCS-7280SRA-48C6\nSoftware image version: 4.33.1.1F", "arista_eos"},
		{"arista_eos", "Cisco Nexus Operating System (NX-OS) Software", "cisco_nxos"},
		{"arista_eos", "Cisco IOS XE Software, Version 17.09.04a", "cisco_iosxe"},
		{"arista_eos", "Cisco IOS Software, C2960X Software", "cisco_ios"},
		{"arista_eos", "some future platform", ""},
		{"juniper_junos", "Hostname: lab-mx1\nModel: mx204\nJunos: 23.4R2.13", "juniper_junos"},
		{"cisco_asa", "Cisco Adaptive Security Appliance Software Version 9.18(4)", "cisco_asa"},
		{"extreme_exos", "ExtremeXOS (X440-G2-48p-10G4) version 22.7.2.4", "extreme_exos"},
		{"aruba_cx", "ArubaOS-CX Version : XL.10.00.0002C-1-g1b84ef2", "aruba_cx"},
		{"aruba_procurve", "HP J9729A 2920-24G Switch, revision KA.16.09.0022", "aruba_procurve"},
		// Real `show version` output from an ArubaOS-Switch 3810M,
		// captured live (2026-08-21): no vendor name text anywhere,
		// only the "Image stamp:" label the classifier now also
		// matches on.
		{"aruba_procurve", "Image stamp:\n /ws/swbuildm/rel_ajanta_qaoff/code/build/bom(swbuildm_rel_ajanta_qaoff_rel_ajanta)\n\t\tApr 10 2023 23:56:39\n\t\tKB.16.10.0024\n\t\t362\nBoot Image:     Secondary", "aruba_procurve"},
		{"mikrotik_routeros", "version: 7.15.3 (stable)\nplatform: MikroTik", "mikrotik_routeros"},
		{"linux", "Linux lab-host 6.8.0 #1 SMP x86_64 GNU/Linux", "linux"},
	}
	for _, c := range cases {
		classes := classesFor(t, c.viaPlatform)
		if got := classify(c.out, classes); got != c.want {
			t.Errorf("classify(via %s probe, %q) = %q, want %q", c.viaPlatform, c.out, got, c.want)
		}
	}
}
