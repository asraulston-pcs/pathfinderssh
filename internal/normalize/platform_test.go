// internal/normalize/platform_test.go
package normalize

import "testing"

func TestPlatformFromDescription(t *testing.T) {
	cases := []struct {
		descr string
		want  string
	}{
		{"Arista DCS-7280SRA-48C6\nSoftware image version: 4.33.1.1F", "arista_eos"},
		{"Cisco Nexus Operating System (NX-OS) Software", "cisco_nxos"},
		{"Cisco IOS XE Software, Version 17.09.04a", "cisco_iosxe"},
		{"Cisco IOS Software, C2960X Software", "cisco_ios"},
		{"Cisco Adaptive Security Appliance Software Version 9.18(4)", "cisco_asa"},
		{"Hostname: lab-mx1\nModel: mx204\nJunos: 23.4R2.13", "juniper_junos"},
		{"Linux lab-host 6.8.0 #1 SMP x86_64 GNU/Linux", "linux"},
		{"", ""},
		{"some future platform", ""},

		// Real ArubaOS-CX LLDP System-Descriptions, captured live
		// (2026-08-26). Older firmware carries an "Aruba" vendor string;
		// HPE's post-rebrand firmware says "HPE ANW" instead.
		{"Aruba JL717C  LL.10.10.1030", "aruba_cx"},
		{"Aruba JL635A  GL.10.13.1090", "aruba_cx"},
		{"Aruba R8S92A  FL.10.10.1090", "aruba_cx"},
		{"HPE ANW S3L76A AL.10.16.1040", "aruba_cx"},

		// Real ArubaOS-Switch (ProVision) LLDP System-Descriptions,
		// captured live (2026-08-26). Must NOT be misclassified as
		// aruba_cx even though both start with "Aruba".
		{"Aruba JL357A 2540-48G-PoE+-4SFP+ Switch, revision YC.16.10.0024, ROM YC.16.01.0002 (/ws/swbuildm/rel_ajanta_qaoff/code/build/cpm(swbuildm_rel_ajanta_qaoff_rel_ajanta))", "aruba_procurve"},
		{"HP J9729A 2920-24G Switch, revision KA.16.09.0022", "aruba_procurve"},

		// Real ExtremeXOS and HPE Comware LLDP System-Descriptions,
		// captured live (2026-08-26).
		{"ExtremeXOS (X440G2-24p-10G4) version 30.2.1.8 30.2.1.8 by release-manager on Tue Apr 30 19:51:20 EDT 2019", "extreme_exos"},
		{"HP Comware Platform Software, Software Version 5.20.99 Release 5501P36\nHP 5500-48G-PoE+-4SFP HI Switch with 2 Interface Slots\nCopyright (c) 2010-2018 Hewlett Packard Enterprise Development LP", "hp_comware"},
	}
	for _, c := range cases {
		if got := PlatformFromDescription(c.descr); got != c.want {
			t.Errorf("PlatformFromDescription(%q) = %q, want %q", c.descr, got, c.want)
		}
	}
}
