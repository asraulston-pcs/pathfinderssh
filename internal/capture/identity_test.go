// internal/capture/identity_test.go
//
// One device must be one device, whatever it ends up being called.
//
// A capture has two names for the same box and they are routinely different:
// the RUN IDENTITY, which is the string the caller's device list used, and the
// CANONICAL NAME, which is what the binding store or the device's own prompt
// says and which is the storage key. Dial 172.16.1.2, resolve it to
// lab-r1.lab.example, and both are in play for the rest of the visit.
//
// The rule these tests pin is that events key on the identity and only on the
// identity. crawlrun learned it the expensive way — terminal events keyed on
// hostname while queue events keyed on identity, so with a domain suffix set
// one device produced two rows and nobody could see it until a table made it
// visible. capturerun's header records that lesson; these tests are what stop
// it being a comment.
package capture_test

import (
	"context"
	"testing"

	"github.com/scottpeterman/pathfinderssh/internal/capture"
	"github.com/scottpeterman/pathfinderssh/internal/capturerun"
	"github.com/scottpeterman/pathfinderssh/internal/credres"
	"github.com/scottpeterman/pathfinderssh/internal/fakedev"
)

// A resolved device produces exactly one device record and one row per type,
// with the platform and the canonical name stamped onto that row.
//
// Every symptom checked here comes from the same cause. When capture events
// key on the canonical and device events key on the identity, the run holds
// two device records for one box; the platform stamp walks the rows belonging
// to the identity and never reaches the row filed under the canonical, so the
// column a UI would show reads blank while the engine knew the answer all
// along.
func TestEveryEventKeysOnTheRunIdentity(t *testing.T) {
	srv := start(t, lab("lab-r1"))
	b := credres.NewMemoryBindings()
	if err := b.Bind("lab-r1.lab.example", "172.16.1.2", "lab-r1"); err != nil {
		t.Fatalf("bind: %v", err)
	}
	run := capturerun.New()
	e, _ := engine(t, capture.Config{
		Dial:     dialerFor(t, map[string]*fakedev.Server{"172.16.1.2": srv}),
		Specs:    []capture.Spec{capture.RunningConfig, capture.Inventory},
		Bindings: b,
		Emit:     run.Emit(),
	})

	res := e.Capture(context.Background(), []capture.Device{{Target: "172.16.1.2"}})
	run.Finish()

	if c := run.Counts(); c.Devices != 1 {
		t.Errorf("Counts.Devices = %d for one device; the run is keying events "+
			"on more than one string for the same box", c.Devices)
	}
	rows := run.RowsSorted()
	if len(rows) != 2 {
		t.Fatalf("got %d rows for one device and two capture types, want 2", len(rows))
	}
	for _, r := range rows {
		if r.Identity != "172.16.1.2" {
			t.Errorf("row %s keyed on %q, want the run identity", r.Type, r.Identity)
		}
		if r.Name != "lab-r1.lab.example" {
			t.Errorf("row %s Name = %q, want the canonical name stamped on",
				r.Type, r.Name)
		}
		if r.Platform != "cisco_ios" {
			t.Errorf("row %s Platform = %q; the fingerprint never reached the row",
				r.Type, r.Platform)
		}
	}

	// The Result set is what a caller reconciles against the list it asked
	// for. Identity is that side of the pair; Device is where it landed.
	for _, r := range res {
		if r.Identity != "172.16.1.2" {
			t.Errorf("Result %s Identity = %q; a caller cannot match this back "+
				"to the device it asked for", r.Type, r.Identity)
		}
		if r.Device != "lab-r1.lab.example" {
			t.Errorf("Result %s Device = %q, want the canonical storage key",
				r.Type, r.Device)
		}
	}
}

// The failure path and the success path must agree on the key, or a device
// whose config stored and whose inventory failed splits into two rows — and
// the split shows up only when something goes wrong, which is the worst time
// to start doubting the table.
func TestFailedAndSucceededCapturesShareOneIdentity(t *testing.T) {
	cfg := lab("lab-r1")
	// Inventory is answered by the device's rejection text rather than by
	// output, which is a command failure rather than a device failure.
	delete(cfg.Commands, "show inventory")
	srv := start(t, cfg)

	b := credres.NewMemoryBindings()
	if err := b.Bind("lab-r1.lab.example", "172.16.1.2"); err != nil {
		t.Fatalf("bind: %v", err)
	}
	run := capturerun.New()
	e, _ := engine(t, capture.Config{
		Dial:     dialerFor(t, map[string]*fakedev.Server{"172.16.1.2": srv}),
		Specs:    []capture.Spec{capture.RunningConfig, capture.Inventory},
		Bindings: b,
		Emit:     run.Emit(),
	})
	e.Capture(context.Background(), []capture.Device{{Target: "172.16.1.2"}})
	run.Finish()

	if c := run.Counts(); c.Devices != 1 {
		t.Errorf("Counts.Devices = %d, want 1", c.Devices)
	}
	seen := map[string]bool{}
	for _, r := range run.RowsSorted() {
		seen[r.Identity] = true
	}
	if len(seen) != 1 {
		t.Errorf("rows keyed under %d identities for one device: %v", len(seen), seen)
	}
}

// A device that is never reached still owes one row per selected type. This is
// the same invariant from the other end: an unreachable device that
// contributed no rows is indistinguishable from one nobody asked about.
func TestAnUnreachableDeviceStillFillsItsRows(t *testing.T) {
	run := capturerun.New()
	e, _ := engine(t, capture.Config{
		Dial:  dialerFor(t, map[string]*fakedev.Server{}),
		Specs: []capture.Spec{capture.RunningConfig, capture.StartupConfig, capture.Inventory},
		Emit:  run.Emit(),
	})
	e.Capture(context.Background(), []capture.Device{{Target: "lab-r9"}})
	run.Finish()

	rows := run.RowsSorted()
	if len(rows) != 3 {
		t.Fatalf("got %d rows for an unreachable device with 3 types selected, want 3", len(rows))
	}
	for _, r := range rows {
		if r.Identity != "lab-r9" {
			t.Errorf("row %s keyed on %q, want the run identity", r.Type, r.Identity)
		}
		if r.Detail == "" {
			t.Errorf("row %s failed with no reason on the row", r.Type)
		}
	}
	if c := run.Counts(); c.DevicesFailed != 1 || c.Failed != 3 {
		t.Errorf("Counts = %+v, want 1 failed device and 3 failed pairs", c)
	}
}
