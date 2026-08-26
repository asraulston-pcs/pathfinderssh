// internal/normalize/platform.go
//
// Platform from an LLDP system description.
//
// CDP carries a Platform field. LLDP does not — it carries a free-text system
// description, and the platform is in there as prose. Every LLDP template in
// this codebase captures the description and none of them capture a platform,
// which is why a neighbor's platform came back empty on every LLDP-only
// device: Arista and Junos both speak LLDP, so an Arista looking at a Juniper
// (or at another Arista) reported nothing at all.
//
// The value matters before the connection exists. Platform-scoped credentials
// and platform-matched jump rules both want to know what a device is *before*
// dialing it, and the neighbor's claim is the only evidence available that
// early. A fingerprint is not a substitute — it arrives after the connection
// that the platform was supposed to inform.
//
// Matching is against the vendor's own boilerplate, which is stable in the
// parts used here: vendors change model numbers and version strings
// constantly, and the company name essentially never.
package normalize

import "regexp"

// platformPatterns maps a system description onto the same platform tokens
// the fingerprinter produces, so a claimed platform and a fingerprinted one
// are the same vocabulary and can be compared.
//
// Order matters. The more specific product lines come first, since "Cisco
// Nexus Operating System" also contains "Cisco".
var platformPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"arista_eos", regexp.MustCompile(`(?i)\bArista\b`)},
	{"cisco_nxos", regexp.MustCompile(`(?i)NX-OS|\bNexus\b`)},
	{"cisco_iosxe", regexp.MustCompile(`(?i)IOS[ \-_]?XE`)},
	{"cisco_ios", regexp.MustCompile(`(?i)Cisco IOS`)},
	{"cisco_asa", regexp.MustCompile(`(?i)Adaptive Security Appliance`)},
	{"juniper_junos", regexp.MustCompile(`(?i)\bJunos\b|\bJuniper\b`)},

	// The four patterns below are new (2026-08-26) -- these platforms were
	// added to this codebase's plan/fingerprint tables well after this
	// file was written, and never got a description pattern of their own,
	// so a neighbor claiming to be any of them always reported an empty
	// platform. All four confirmed against real LLDP System-Description
	// text captured live this session.
	//
	// aruba_cx checked before aruba_procurve and anchored tight on
	// purpose: both vendors' descriptions start with "Aruba" (or, on
	// HPE's post-rebrand firmware, "HPE ANW"), and the only reliable
	// difference seen so far is that a CX description is JUST
	// "<vendor> <PID> <CC.NN.NN.NNNN version>" and nothing else --
	// confirmed live as "Aruba JL717C  LL.10.10.1030" and "Aruba R8S92A
	// FL.10.10.1090" (older firmware) and "HPE ANW S3L76A AL.10.16.1040"
	// (post-rebrand). A ProVision description always carries more text
	// after the model -- "Switch, revision ..., ROM ..." -- so anchoring
	// end-to-end (^...$) is what keeps this from also matching that.
	{"aruba_cx", regexp.MustCompile(`(?i)^(?:Aruba|HPE\s+ANW)\s+\S+\s+[A-Z]{2}\.\d{2}\.\d{2}\.\d{4}\s*$`)},
	// ExtremeXOS: "ExtremeXOS (X440G2-24p-10G4) version 30.2.1.8 ...",
	// confirmed live. Checked before aruba_procurve's broad HP/Switch
	// match below for the same reason hp_comware is: neither has any
	// legitimate overlap with an Aruba/HP product line, so putting them
	// first costs nothing and closes off any future collision.
	{"extreme_exos", regexp.MustCompile(`(?i)\bExtremeXOS\b`)},
	// HPE Comware: "HP Comware Platform Software, Software Version
	// 5.20.99 Release 5501P36\nHP 5500-48G-PoE+-4SFP HI Switch with 2
	// Interface Slots\n...", confirmed live. MUST be checked before
	// aruba_procurve: a real Comware description's second line names its
	// own chassis as "HP <model> ... Switch", which is indistinguishable
	// from a genuine ProVision description under the broader HP/Switch
	// pattern below -- confirmed live the hard way, as a first cut of
	// this table classified a real Comware switch as aruba_procurve.
	{"hp_comware", regexp.MustCompile(`(?i)\bComware\b`)},
	// ArubaOS-Switch (ProVision): "Aruba JL357A 2540-48G-PoE+-4SFP+
	// Switch, revision YC.16.10.0024, ROM YC.16.01.0002 (...)" and "HP
	// J9729A 2920-24G Switch, revision KA.16.09.0022", both confirmed
	// live. Some firmware (see fingerprint.go's aruba_procurve probe)
	// reports no vendor name at all, only "Image stamp:" -- that shape
	// has no usable text here either, and stays an honest empty answer
	// rather than a guess.
	{"aruba_procurve", regexp.MustCompile(`(?i)\b(?:Aruba|HP)\b.*\bSwitch\b|\bProCurve\b`)},

	{"linux", regexp.MustCompile(`(?i)\bLinux\b`)},
}

// PlatformFromDescription returns a platform token for an LLDP system
// description, or empty when nothing matches.
//
// Empty is a real answer and the caller must treat it as "unknown" rather
// than as a default. Guessing here would be worse than not knowing: a
// platform-scoped credential offered to a device that is not that platform is
// a wasted authentication attempt against every such device in the fabric.
func PlatformFromDescription(descr string) string {
	if descr == "" {
		return ""
	}
	for _, p := range platformPatterns {
		if p.re.MatchString(descr) {
			return p.name
		}
	}
	return ""
}
