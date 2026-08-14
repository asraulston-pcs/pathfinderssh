// internal/netexec/fingerprint.go
// Platform fingerprinting: identify the NOS by combining paging-disable
// probes with a version command.
//
// The trick that makes this cheap: paging-disable commands are a clean
// binary signal. The right one for the platform produces no output; the
// wrong one produces an error marker ("% Invalid input", "syntax error",
// "Unrecognized command", ...). One probe therefore narrows to a family,
// and the family's version command refines to the exact platform.
//
// HARD CONSTRAINT on the probe table: every command in it must be
// exec-level, read-only, and side-effect-free on every platform it might
// be blindly thrown at. Paging changes are session-local on all listed
// platforms. Never add a probe that could touch config.
package netexec

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// Platform is the result of a successful (or best-effort) fingerprint.
type Platform struct {
	// Name is the detected platform, e.g. "arista_eos", "cisco_iosxe",
	// "juniper_junos". "unknown" when nothing matched.
	Name string
	// PagingDisable is the paging command this platform accepted ("" for
	// platforms without paging, e.g. mikrotik, linux).
	PagingDisable string
	// VersionCommand is the command whose output identified the platform.
	VersionCommand string
	// VersionOutput is that command's cleaned output, kept so callers can
	// mine model/version details without a second round trip.
	VersionOutput string
}

// cliErrorRe matches the error markers network CLIs emit for an
// unrecognized command.
var cliErrorRe = regexp.MustCompile(`(?im)^\s*(%|\^|error[:\s]|invalid input|syntax error|unknown command|unrecognized command|bad command|incomplete command|expecting|failure:)|command not found`)

// isCLIError reports whether output looks like a command rejection rather
// than real output.
func isCLIError(out string) bool {
	return cliErrorRe.MatchString(out)
}

// versionClass maps version-command output to a platform name.
type versionClass struct {
	name  string
	match *regexp.Regexp
}

// probe is one fingerprint attempt: a paging command to try (optional) and
// a version command whose output is classified.
type probe struct {
	paging     string
	versionCmd string
	classes    []versionClass
}

// probes are ordered by prevalence: the terminal-length-0 family first
// (IOS/IOS-XE/NX-OS/EOS all take it), then Junos, ASA, and the long tail.
var probes = []probe{
	{
		paging:     "terminal length 0",
		versionCmd: "show version",
		classes: []versionClass{
			{"arista_eos", regexp.MustCompile(`(?i)\bArista\b`)},
			{"cisco_nxos", regexp.MustCompile(`(?i)NX-OS|Nexus`)},
			{"cisco_iosxe", regexp.MustCompile(`(?i)IOS[ -]XE`)},
			{"cisco_ios", regexp.MustCompile(`(?i)Cisco IOS Software`)},
		},
	},
	{
		paging:     "set cli screen-length 0",
		versionCmd: "show version",
		classes: []versionClass{
			{"juniper_junos", regexp.MustCompile(`(?i)JUNOS|Junos`)},
		},
	},
	{
		paging:     "terminal pager 0",
		versionCmd: "show version",
		classes: []versionClass{
			{"cisco_asa", regexp.MustCompile(`(?i)Adaptive Security Appliance`)},
		},
	},
	{
		paging:     "no page",
		versionCmd: "show version",
		classes: []versionClass{
			{"aruba_procurve", regexp.MustCompile(`(?i)ProCurve|Aruba|HP`)},
		},
	},
	{
		paging:     "screen-length disable",
		versionCmd: "display version",
		classes: []versionClass{
			{"huawei_vrp", regexp.MustCompile(`(?i)Huawei|VRP`)},
			{"hp_comware", regexp.MustCompile(`(?i)Comware|HPE?`)},
		},
	},
	{
		// No paging concept on these; version command alone.
		paging:     "",
		versionCmd: "/system resource print",
		classes: []versionClass{
			{"mikrotik_routeros", regexp.MustCompile(`(?i)RouterOS|MikroTik`)},
		},
	},
	{
		paging:     "",
		versionCmd: "uname -a",
		classes: []versionClass{
			{"linux", regexp.MustCompile(`(?i)\bLinux\b`)},
			{"bsd_or_mac", regexp.MustCompile(`(?i)Darwin|BSD`)},
		},
	},
}

// classify returns the platform name for version output, or "".
func classify(out string, classes []versionClass) string {
	for _, c := range classes {
		if c.match.MatchString(out) {
			return c.name
		}
	}
	return ""
}

// Fingerprint identifies the platform of an open session. On success the
// accepted paging-disable command has already been applied, so the session
// is ready for long-output commands. When every probe misses, it returns a
// best-effort Platform{Name: "unknown"} carrying whatever paging command
// stuck and the last usable version output, plus a nil error — callers
// distinguish by Name. A non-nil error means the session itself broke.
func Fingerprint(ctx context.Context, s *Session) (*Platform, error) {
	var (
		bestPaging  string
		bestVersion string
		bestVerCmd  string
	)
	for _, p := range probes {
		if p.paging != "" {
			out, err := s.Run(ctx, p.paging)
			if err != nil {
				return nil, fmt.Errorf("fingerprint probe %q: %w", p.paging, err)
			}
			if isCLIError(out) {
				continue // wrong family — next probe
			}
			bestPaging = p.paging
		}
		out, err := s.Run(ctx, p.versionCmd)
		if err != nil {
			return nil, fmt.Errorf("fingerprint probe %q: %w", p.versionCmd, err)
		}
		if isCLIError(out) {
			continue
		}
		if strings.TrimSpace(out) != "" {
			bestVersion, bestVerCmd = out, p.versionCmd
		}
		if name := classify(out, p.classes); name != "" {
			return &Platform{
				Name:           name,
				PagingDisable:  p.paging,
				VersionCommand: p.versionCmd,
				VersionOutput:  out,
			}, nil
		}
	}
	return &Platform{
		Name:           "unknown",
		PagingDisable:  bestPaging,
		VersionCommand: bestVerCmd,
		VersionOutput:  bestVersion,
	}, nil
}
