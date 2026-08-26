// internal/tfsm/aruba_procurve_test.go
//
// Regression coverage for a real parse failure hit live (2026-08-21)
// against an ArubaOS-Switch 3810M: a neighbor whose Remote Management
// Address Type is "all802" reports the address as six space-separated hex
// byte pairs rather than a dotted-quad IP, which the original MGMT_ADDRESS
// pattern (\S+) could not match.
package tfsm

import "testing"

const arubaProcurveAll802Block = `
  Local Port   : 48
  ChassisType  : mac-address
  ChassisId    : 020000-005120
  PortType     : interface-name
  PortId       : 3:8
  SysName      : lab-exos1
  System Descr : ExtremeXOS (Stack) version 32.6.3.127 32.6.3.127-patch1-8
  PortDescr    :
  Pvid         :

  System Capabilities Supported  : bridge, router
  System Capabilities Enabled    : bridge

  Remote Management Address
     Type    : all802
     Address : 02 00 00 00 51 20

  Poe Plus Information Detail

    Poe Device Type         : Type2 PSE
    Power Source            : Unknown
    Power Priority          : Unknown
    PD Requested Power Value   : 0.0 Watts
    PSE Allocated Power Value  : 0.0 Watts
`

func TestArubaProcurveParsesAnAll802ManagementAddress(t *testing.T) {
	recs, err := Parse("aruba_procurve", "lldp_detail", arubaProcurveAll802Block)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	rec := recs[0]
	if rec["LOCAL_INTERFACE"] != "48" {
		t.Errorf("LOCAL_INTERFACE = %q, want %q", rec["LOCAL_INTERFACE"], "48")
	}
	if rec["MGMT_ADDRESS"] != "02 00 00 00 51 20" {
		t.Errorf("MGMT_ADDRESS = %q, want %q", rec["MGMT_ADDRESS"], "02 00 00 00 51 20")
	}
	if rec["NEIGHBOR_NAME"] != "lab-exos1" {
		t.Errorf("NEIGHBOR_NAME = %q, want %q", rec["NEIGHBOR_NAME"], "lab-exos1")
	}
}

// Regression coverage for a real parse failure hit live (2026-08-26)
// against a real ArubaOS-Switch stack: MED detail on some neighbors
// carries fields this template never saw when it was built --
// "Media Policy Vlan id   :150" is reproduced verbatim below; it and
// "Power Requested        :25.0 W" (a real second failure seen the same
// run) both fell into the trailing `^.* -> Error` catch-all and aborted
// the parse for the WHOLE device. The record after the unrecognized line
// must still parse -- that's the proof the catch-all drops rather than
// aborts.
const arubaProcurveUnknownMEDFieldsBlock = `
  Local Port   : 1
  ChassisType  : mac-address
  ChassisId    : 9c3708-fe4a40
  PortType     : interface-name
  PortId       : 1/1/52
  SysName      : lab-flr2-stack1
  System Descr : HPE ANW S3L76A  AL.10.16.1040
  PortDescr    : LAG1
  Pvid         : 5

  System Capabilities Supported  : bridge, router
  System Capabilities Enabled    : bridge, router

  Remote Management Address
     Type    : ipv4
     Address : 10.237.1.10

  MED Information Detail
     Media Policy Vlan id   :150

  Poe Plus Information Detail

    Poe Device Type         : Type2 PSE
    Power Source            : Unknown
    Power Priority          : Unknown
    Power Requested        :25.0 W
    PD Requested Power Value   : 25.0 Watts
    PSE Allocated Power Value  : 25.0 Watts

--------------------------------------------------------------------------------

  Local Port   : 2
  ChassisType  : mac-address
  ChassisId    : 9c3708-fefa00
  PortType     : interface-name
  PortId       : 6/1/52
  SysName      : lab-flr2-stack2
  System Descr : HPE ANW S3L76A  AL.10.16.1040
  PortDescr    : LAG2
  Pvid         : 5

  System Capabilities Supported  : bridge, router
  System Capabilities Enabled    : bridge, router

  Remote Management Address
     Type    : ipv4
     Address : 10.237.1.11
`

func TestArubaProcurveParsesPastUnknownMEDFields(t *testing.T) {
	recs, err := Parse("aruba_procurve", "lldp_detail", arubaProcurveUnknownMEDFieldsBlock)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2", len(recs))
	}
	if recs[0]["NEIGHBOR_NAME"] != "lab-flr2-stack1" {
		t.Errorf("recs[0] NEIGHBOR_NAME = %q, want %q", recs[0]["NEIGHBOR_NAME"], "lab-flr2-stack1")
	}
	if recs[0]["MGMT_ADDRESS"] != "10.237.1.10" {
		t.Errorf("recs[0] MGMT_ADDRESS = %q, want %q", recs[0]["MGMT_ADDRESS"], "10.237.1.10")
	}
	// The record after the unrecognized MED/PoE lines must survive intact --
	// proof the catch-all dropped the stray lines instead of aborting.
	if recs[1]["NEIGHBOR_NAME"] != "lab-flr2-stack2" {
		t.Errorf("recs[1] NEIGHBOR_NAME = %q, want %q", recs[1]["NEIGHBOR_NAME"], "lab-flr2-stack2")
	}
	if recs[1]["MGMT_ADDRESS"] != "10.237.1.11" {
		t.Errorf("recs[1] MGMT_ADDRESS = %q, want %q", recs[1]["MGMT_ADDRESS"], "10.237.1.11")
	}
}
