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

func TestClassifyVersions(t *testing.T) {
	cases := []struct {
		probeIdx int
		out      string
		want     string
	}{
		{0, "Arista DCS-7280SRA-48C6\nSoftware image version: 4.33.1.1F", "arista_eos"},
		{0, "Cisco Nexus Operating System (NX-OS) Software", "cisco_nxos"},
		{0, "Cisco IOS XE Software, Version 17.09.04a", "cisco_iosxe"},
		{0, "Cisco IOS Software, C2960X Software", "cisco_ios"},
		{0, "some future platform", ""},
		{1, "Hostname: lab-mx1\nModel: mx204\nJunos: 23.4R2.13", "juniper_junos"},
		{2, "Cisco Adaptive Security Appliance Software Version 9.18(4)", "cisco_asa"},
		{5, "version: 7.15.3 (stable)\nplatform: MikroTik", "mikrotik_routeros"},
		{6, "Linux lab-host 6.8.0 #1 SMP x86_64 GNU/Linux", "linux"},
	}
	for _, c := range cases {
		if got := classify(c.out, probes[c.probeIdx].classes); got != c.want {
			t.Errorf("classify(probe %d, %q) = %q, want %q", c.probeIdx, c.out, got, c.want)
		}
	}
}
