// internal/crawler/addressbackfill_test.go
//
// Regression coverage for a real-world claim race, diagnosed live
// (2026-08-26) against a real customer network with no DNS for switch
// hostnames: two parent devices in the SAME depth batch both discover the
// same downstream neighbor. tryClaim admits whichever report reaches it
// first, and used to keep ONLY that report -- if it happened to carry no
// usable management address, a second report of the exact same device
// (different parent, different port, but the same neighbor) that DID carry
// a real address was discarded outright. The device was then dialed by
// name only; with no DNS to resolve that name, it could never be reached,
// even though something in the batch had told the crawler exactly how to
// reach it by IP.
package crawler

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/scottpeterman/pathfinderssh/internal/crawlrun"
	"github.com/scottpeterman/pathfinderssh/internal/fakedev"
	"github.com/scottpeterman/pathfinderssh/internal/sshcore"
)

func startFakeEOS(t *testing.T, prompt string, extraCommands map[string]string) *fakedev.Server {
	t.Helper()
	cmds := map[string]string{
		"terminal length 0": "",
		"show version":      "Arista DCS-7050SX-64\nSoftware image version: 4.20.1F\n",
	}
	for k, v := range extraCommands {
		cmds[k] = v
	}
	srv, err := fakedev.Start(fakedev.Config{
		Prompt:            prompt,
		Commands:          cmds,
		AcceptAnyPassword: true,
	})
	if err != nil {
		t.Fatalf("start fake device: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv
}

func TestNeighborAddressBackfilledFromLaterDuplicateClaim(t *testing.T) {
	leaf := startFakeEOS(t, "shared-leaf#", nil)

	// core-a discovers "shared-leaf" but its LLDP detail has no Management
	// Address line at all -- the exact shape a summary-only collection step
	// or an address-less link produces in the wild.
	coreA := startFakeEOS(t, "core-a#", map[string]string{
		"show lldp neighbors detail": `Interface Ethernet1 detected 1 LLDP neighbors:

  Neighbor 0011.2233.4455 age 10 seconds
  Discovered 1 day, 00:00:00 ago; Last changed 1 day ago
  - Chassis ID type: MAC address (4)
    Chassis ID     : 0011.2233.4455
  - Port ID type: Interface name(5)
    Port ID     : "Ethernet49/1"
  - System Name: "shared-leaf"
  - System Description: "Lab NOS 4.20"
`,
	})

	// core-b discovers the SAME shared-leaf, on a different local port, and
	// DOES carry its management address.
	coreB := startFakeEOS(t, "core-b#", map[string]string{
		"show lldp neighbors detail": `Interface Ethernet7 detected 1 LLDP neighbors:

  Neighbor 0011.2233.4455 age 10 seconds
  Discovered 1 day, 00:00:00 ago; Last changed 1 day ago
  - Chassis ID type: MAC address (4)
    Chassis ID     : 0011.2233.4455
  - Port ID type: Interface name(5)
    Port ID     : "Ethernet49/2"
  - System Name: "shared-leaf"
  - System Description: "Lab NOS 4.20"
  Management Address        : 10.20.0.99
`,
	})

	servers := map[string]*fakedev.Server{"core-a": coreA, "core-b": coreB}
	dialFn := func(ctx context.Context, tgt DialTarget) (*sshcore.Client, error) {
		if srv, ok := servers[tgt.Target]; ok {
			return srv.Dial("lab", "lab")
		}
		if tgt.Target == "10.20.0.99" {
			return leaf.Dial("lab", "lab")
		}
		// Mirrors a real DNS failure dialing "shared-leaf" by name on a
		// network with no record for it: a genuine *net.OpError wrapping a
		// *net.DNSError, exactly what sshcore.Dial's net.DialTimeout
		// produces, wrapped the same way ("connect to %s: %w"). Anything
		// less specific than this would not actually exercise
		// shouldRetryByAddr's real type-checking.
		dnsErr := &net.DNSError{Err: "no such host", Name: tgt.Target, IsNotFound: true}
		return nil, fmt.Errorf("connect to %s:22: %w", tgt.Target,
			&net.OpError{Op: "dial", Net: "tcp", Err: dnsErr})
	}

	run := crawlrun.New()
	c := New(Config{
		Dial:     dialFn,
		MaxDepth: 1,
		Log:      func(string, ...any) {},
		Emit:     run.Emit(),
	})
	c.Crawl([]string{"core-a", "core-b"})
	run.Finish()

	for _, row := range run.Rows() {
		if row.Identity != "shared-leaf" {
			continue
		}
		if row.State != crawlrun.StateReached {
			t.Fatalf("shared-leaf state = %v (detail %q), want reached -- "+
				"the second report's address was not backfilled onto the first claim",
				row.State, row.Detail)
		}
		return
	}
	t.Fatal("shared-leaf never appeared in the run at all")
}
