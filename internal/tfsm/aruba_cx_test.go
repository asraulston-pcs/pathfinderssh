// internal/tfsm/aruba_cx_test.go
//
// Regression coverage for a real parse failure hit live (2026-08-21)
// against an ArubaOS-CX 6300-series switch ("lab-cx1"): a neighbor with no
// management address at all reports the field as
// "Neighbor Management-Address    :" with nothing after the colon, which
// the original MGMT_ADDRESS pattern (\S+) could not match -- it requires
// at least one non-whitespace character.
package tfsm

import "testing"

const arubaCXEmptyMgmtAddressBlock = `
Port                           : 1/1/7
Neighbor Entries               : 1
Neighbor Entries Deleted       : 0
Neighbor Entries Dropped       : 0
Neighbor Entries Aged-Out      : 0
Neighbor System-Name           :
Neighbor System-Description    :
Neighbor Chassis-ID            : 02:00:00:00:51:10
Neighbor Management-Address    :
Chassis Capabilities Available :
Chassis Capabilities Enabled   :
Neighbor Port-ID               : 02:00:00:00:51:10
Neighbor Port-Desc             :
TTL                            : 121
`

func TestArubaCXParsesAnEmptyManagementAddress(t *testing.T) {
	recs, err := Parse("aruba_cx", "lldp_detail", arubaCXEmptyMgmtAddressBlock)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	rec := recs[0]
	if rec["LOCAL_INTERFACE"] != "1/1/7" {
		t.Errorf("LOCAL_INTERFACE = %q, want %q", rec["LOCAL_INTERFACE"], "1/1/7")
	}
	if rec["MGMT_ADDRESS"] != "" {
		t.Errorf("MGMT_ADDRESS = %q, want empty", rec["MGMT_ADDRESS"])
	}
	if rec["CHASSIS_ID"] != "02:00:00:00:51:10" {
		t.Errorf("CHASSIS_ID = %q, want %q", rec["CHASSIS_ID"], "02:00:00:00:51:10")
	}
}

// Regression coverage for a real parse failure hit live (2026-08-26)
// against a real ArubaOS-CX 6300 stack: a neighbor's Mac-Phy/EEE block
// ("Neighbor Mac-Phy details", "Neighbor Auto-neg Supported", "Neighbor
// MAU type", "Neighbor EEE information", per-direction wake/echo times)
// matched no rule in the template and fell into the trailing
// `^.* -> Error` catch-all, aborting the parse for the WHOLE capture on
// the first neighbor record -- a switch reporting 16 real LLDP
// neighbors came back with zero. Device names/IPs below are genericized
// from the real capture; the Mac-Phy/EEE block that triggered the bug
// is reproduced verbatim.
const arubaCXMacPhyDetailBlock = `
LLDP Neighbor Information
=========================

Total Neighbor Entries          : 16
Total Neighbor Entries Deleted  : 3
Total Neighbor Entries Dropped  : 0
Total Neighbor Entries Aged-Out : 3

--------------------------------------------------------------------------------

Port                           : 1/1/5
Neighbor Entries               : 1
Neighbor Entries Deleted       : 0
Neighbor Entries Dropped       : 0
Neighbor Entries Aged-Out      : 0
Neighbor System-Name           : lab-idf-sw1
Neighbor System-Description    : Aruba JL357A 2540-48G-PoE+-4SFP+ Switch, revision YC.16.10.0024, ROM YC.16.01.0002 (/ws/swbuildm/rel_ajanta_qaoff/code/build/cpm(swbuildm_rel_ajanta_qaoff_rel_ajanta))
Neighbor Chassis-ID            : 38:10:f0:cf:c4:c0
Neighbor Management-Address    : 10.0.0.45
Chassis Capabilities Available : Bridge, Router
Chassis Capabilities Enabled   : Bridge
Neighbor Port-ID               : 49
Neighbor Port-Desc             : 49
Neighbor Port VLAN ID          : 236
TTL                            : 120

Neighbor Mac-Phy details
Neighbor Auto-neg Supported    : true
Neighbor Auto-Neg Enabled      : true
Neighbor Auto-Neg Advertised   : 1000 BASE_XFD
Neighbor MAU type              : 10 GIGBASESR

Neighbor EEE information       : DOT3
Neighbor TX Wake time          : 0 us
Neighbor RX Wake time          : 0 us
Neighbor Fallback time         : 0 us
Neighbor TX Echo time          : 0 us
Neighbor RX Echo time          : 0 us

--------------------------------------------------------------------------------

Port                           : 1/1/7
Neighbor Entries               : 1
Neighbor Entries Deleted       : 0
Neighbor Entries Dropped       : 0
Neighbor Entries Aged-Out      : 0
Neighbor System-Name           : lab-mdf-sw3
Neighbor System-Description    : Aruba JL357A 2540-48G-PoE+-4SFP+ Switch, revision YC.16.10.0024, ROM YC.16.01.0002 (/ws/swbuildm/rel_ajanta_qaoff/code/build/cpm(swbuildm_rel_ajanta_qaoff_rel_ajanta))
Neighbor Chassis-ID            : 10:4f:58:ee:e2:80
Neighbor Management-Address    : 10.0.0.32
Chassis Capabilities Available : Bridge, Router
Chassis Capabilities Enabled   : Bridge
Neighbor Port-ID               : 49
Neighbor Port-Desc             : 49
Neighbor Port VLAN ID          : 236
TTL                            : 120

Neighbor Mac-Phy details
Neighbor Auto-neg Supported    : true
Neighbor Auto-Neg Enabled      : true
Neighbor Auto-Neg Advertised   : 1000 BASE_XFD
Neighbor MAU type              : 10 GIGBASESR

Neighbor EEE information       : DOT3
Neighbor TX Wake time          : 0 us
Neighbor RX Wake time          : 0 us
Neighbor Fallback time         : 0 us
Neighbor TX Echo time          : 0 us
Neighbor RX Echo time          : 0 us

--------------------------------------------------------------------------------
`

func TestArubaCXParsesPastMacPhyDetailBlock(t *testing.T) {
	recs, err := Parse("aruba_cx", "lldp_detail", arubaCXMacPhyDetailBlock)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2", len(recs))
	}
	if recs[0]["NEIGHBOR_NAME"] != "lab-idf-sw1" {
		t.Errorf("recs[0] NEIGHBOR_NAME = %q, want %q", recs[0]["NEIGHBOR_NAME"], "lab-idf-sw1")
	}
	if recs[0]["LOCAL_INTERFACE"] != "1/1/5" {
		t.Errorf("recs[0] LOCAL_INTERFACE = %q, want %q", recs[0]["LOCAL_INTERFACE"], "1/1/5")
	}
	if recs[1]["NEIGHBOR_NAME"] != "lab-mdf-sw3" {
		t.Errorf("recs[1] NEIGHBOR_NAME = %q, want %q", recs[1]["NEIGHBOR_NAME"], "lab-mdf-sw3")
	}
	if recs[1]["MGMT_ADDRESS"] != "10.0.0.32" {
		t.Errorf("recs[1] MGMT_ADDRESS = %q, want %q", recs[1]["MGMT_ADDRESS"], "10.0.0.32")
	}
}
