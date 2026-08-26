// internal/crawler/unwrap_test.go
package crawler

import (
	"strings"
	"testing"
)

// Regression coverage for a real bug hit live (2026-08-26) against a real
// ArubaOS-CX 6300 stack: unwrapWrapped was built entirely around Junos's
// tabular LLDP output, where a wrapped row is a bare token fragment with no
// label. ArubaOS-CX's "show lldp neighbor-info detail" is field:value
// lines instead, and a long System-Description (150+ chars, common on this
// platform) tripped the same wrap-detection heuristic a genuinely wrapped
// Junos row would. Every following field line then got glued onto the
// description with no separator -- Chassis-ID, Management-Address, and
// everything up to TTL merged into one line -- so MGMT_ADDRESS, the field
// the dial-by-address fallback depends on, was never its own line to match
// and came back empty. Short descriptions (every other platform sharing
// this command) never reached the false-trigger width, which is why the
// failure correlated with description length rather than platform.
//
// Real capture (device names, IPs, and MACs genericized; every other field,
// and critically the exact line lengths that trigger the wrap-detection
// bug, are reproduced verbatim). Two long-description neighbors followed
// by one short-description neighbor, to catch the fix under- or
// over-correcting in either direction.
const realArubaCXLLDPDetail = `Port                           : 1/1/5
Neighbor Entries               : 1
Neighbor Entries Deleted       : 2
Neighbor Entries Dropped       : 0
Neighbor Entries Aged-Out      : 2
Neighbor System-Name           : IDF1-SW7
Neighbor System-Description    : Aruba JL357A 2540-48G-PoE+-4SFP+ Switch, revision YC.16.10.0024, ROM YC.16.01.0002 (/ws/swbuildm/rel_ajanta_qaoff/code/build/cpm(swbuildm_rel_ajanta_qaoff_rel_ajanta))
Neighbor Chassis-ID            : 00:11:22:aa:bb:11
Neighbor Management-Address    : 10.0.0.146
Chassis Capabilities Available : Bridge, Router
Chassis Capabilities Enabled   : Bridge
Neighbor Port-ID               : 49
Neighbor Port-Desc             : 49
Neighbor Port VLAN ID          : 236
TTL                            : 120

--------------------------------------------------------------------------------

Port                           : 1/1/7
Neighbor Entries               : 1
Neighbor Entries Deleted       : 3
Neighbor Entries Dropped       : 0
Neighbor Entries Aged-Out      : 3
Neighbor System-Name           : MDF-SW4
Neighbor System-Description    : Aruba JL357A 2540-48G-PoE+-4SFP+ Switch, revision YC.16.10.0024, ROM YC.16.01.0002 (/ws/swbuildm/rel_ajanta_qaoff/code/build/cpm(swbuildm_rel_ajanta_qaoff_rel_ajanta))
Neighbor Chassis-ID            : 00:11:22:aa:bb:12
Neighbor Management-Address    : 10.0.0.133
Chassis Capabilities Available : Bridge, Router
Chassis Capabilities Enabled   : Bridge
Neighbor Port-ID               : 49
Neighbor Port-Desc             : 49
Neighbor Port VLAN ID          : 236
TTL                            : 120

--------------------------------------------------------------------------------

Port                           : 1/1/30
Neighbor Entries               : 1
Neighbor Entries Deleted       : 2
Neighbor Entries Dropped       : 0
Neighbor Entries Aged-Out      : 2
Neighbor System-Name           : LAB-SCALE-SW01
Neighbor System-Description    : Aruba R8S92A  FL.10.10.1090
Neighbor Chassis-ID            : 00:11:22:aa:bb:13
Neighbor Management-Address    : 10.0.0.121
Chassis Capabilities Available : Bridge, Router
Chassis Capabilities Enabled   : Bridge, Router
Neighbor Port-ID               : 2/1/24
Neighbor Port-Desc             : LAG1 to Core
Neighbor Port VLAN ID          : 236
TTL                            : 120
`

func TestUnwrapDoesNotCorruptArubaCXFieldValueOutput(t *testing.T) {
	joined, joins := unwrapWrapped(realArubaCXLLDPDetail)
	if joins != 0 {
		t.Fatalf("got %d join(s), want 0 -- field:value lines must never be treated as wrapped continuations\n%s",
			joins, joined)
	}
	if joined != realArubaCXLLDPDetail {
		t.Fatalf("text changed even though joins=0:\nwant:\n%s\ngot:\n%s", realArubaCXLLDPDetail, joined)
	}
}

// The original Junos scenario this file was built for (see the file's own
// doc comment): a table row wrapped mid-token, with no label anywhere, must
// still be rejoined. This is the case fieldLabelRe must NOT swallow.
func TestUnwrapStillRejoinsGenuineJunosRowWrap(t *testing.T) {
	// Column widths chosen so both wrapped rows sit at the same length,
	// satisfying wrapWidth's Signal 2 (repeated max width, every occurrence
	// followed by a continuation, and the last line is not at that width).
	const wrapped = `ge-4/0/3   ae40  74:83:ef:96:54:d2  a fairly long port description field b17-s105a-be05-
a7280r.lab.example.edu
xe-4/0/4   ae40  bc:2c:e6:b7:0c:d9  a fairly long port description field brdr10.site2.la
b.example.net
et-0/0/1   -     00:11:22:33:44:55  short
`
	joined, joins := unwrapWrapped(wrapped)
	if joins != 2 {
		t.Fatalf("got %d join(s), want 2 (one per wrapped row)\n%s", joins, joined)
	}
	if !strings.Contains(joined, "b17-s105a-be05-a7280r.lab.example.edu") {
		t.Errorf("first row was not rejoined:\n%s", joined)
	}
	if !strings.Contains(joined, "brdr10.site2.lab.example.net") {
		t.Errorf("second row was not rejoined:\n%s", joined)
	}
}
