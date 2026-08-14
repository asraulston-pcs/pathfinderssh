// internal/sessiondial/default_test.go
//
// A session that names no credential asks the store what it would use.
//
// These run against internal/fakedev — a real SSH server in-process — so the
// question being answered is "does it actually authenticate with the default",
// not "was the right function called".
package sessiondial_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/scottpeterman/pathfinderssh/internal/fakedev"
	"github.com/scottpeterman/pathfinderssh/internal/sessiondial"
	"github.com/scottpeterman/pathfinderssh/internal/sessions"
)

// The case this whole feature exists for: an imported node with nothing on it
// but a name and an address connects, because the store had a default.
func TestANodeWithNoCredentialUsesTheStoreDefault(t *testing.T) {
	srv := startDevice(t, "lab-r1")

	n := nodeFor(srv)
	n.Username = ""
	n.Password = ""
	n.AuthType = ""
	n.Credential = "" // exactly what a map import produces

	var asked []string
	opts := sessiondial.Options{
		Credentials: func(ref string) (sessiondial.Credential, error) {
			asked = append(asked, ref)
			return sessiondial.Credential{
				Username: "admin",
				AuthType: sessions.AuthPassword,
				Password: "lab",
			}, nil
		},
	}

	tp, err := sessiondial.Connect(context.Background(), n, opts)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer tp.Close()

	if len(asked) != 1 || asked[0] != "" {
		t.Fatalf("store was asked %q, want exactly one empty ref", asked)
	}
	readUntil(t, tp, "lab-r1#", 5*time.Second)
}

// A store with no default answers with nothing, and that has to remain manual
// auth rather than becoming an error. This is the whole of the old behaviour,
// and it is what anyone who never sets a default keeps.
func TestNoDefaultLeavesTheNodeOnItsOwnCredentials(t *testing.T) {
	srv := startDevice(t, "lab-r1")

	n := nodeFor(srv) // carries admin/lab on the node itself
	n.Credential = ""

	opts := sessiondial.Options{
		Credentials: func(ref string) (sessiondial.Credential, error) {
			return sessiondial.Credential{}, nil
		},
	}

	tp, err := sessiondial.Connect(context.Background(), n, opts)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer tp.Close()
	readUntil(t, tp, "lab-r1#", 5*time.Second)
}

// A store that rejects an empty ref — which is what every lookup written before
// this existed does — must not break every ad-hoc session in the application.
// "" names nothing, so it cannot be missing.
func TestAStoreThatRefusesAnEmptyRefIsNotAnError(t *testing.T) {
	srv := startDevice(t, "lab-r1")

	n := nodeFor(srv)
	n.Credential = ""

	opts := sessiondial.Options{
		Credentials: func(ref string) (sessiondial.Credential, error) {
			return sessiondial.Credential{}, errNoSuchCredential
		},
	}

	tp, err := sessiondial.Connect(context.Background(), n, opts)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer tp.Close()
	readUntil(t, tp, "lab-r1#", 5*time.Second)
}

// What the node says still wins. The default fills a blank; it does not
// override a choice somebody made.
func TestANamedCredentialIsNotReplacedByTheDefault(t *testing.T) {
	srv := startDevice(t, "lab-r1")

	n := nodeFor(srv)
	n.Username = ""
	n.Password = ""
	n.AuthType = ""
	n.Credential = "lab-readonly"

	var asked []string
	opts := sessiondial.Options{
		Credentials: func(ref string) (sessiondial.Credential, error) {
			asked = append(asked, ref)
			return sessiondial.Credential{
				Username: "admin",
				AuthType: sessions.AuthPassword,
				Password: "lab",
			}, nil
		},
	}

	tp, err := sessiondial.Connect(context.Background(), n, opts)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer tp.Close()

	if len(asked) != 1 || asked[0] != "lab-readonly" {
		t.Fatalf("store was asked %q, want one lookup of lab-readonly", asked)
	}
}

// A node with no jump host must not resolve a jump credential at all. It used
// to, which meant a stale Jump.Credential naming a deleted entry refused a
// direct connection for a reason that had nothing to do with it.
func TestNoJumpHostMeansNoJumpLookup(t *testing.T) {
	srv := startDevice(t, "lab-r1")

	n := nodeFor(srv)
	n.Credential = "lab-readonly"
	n.Jump.Credential = "a-credential-that-is-long-gone"

	var asked []string
	opts := sessiondial.Options{
		Credentials: func(ref string) (sessiondial.Credential, error) {
			asked = append(asked, ref)
			if ref == "lab-readonly" {
				return sessiondial.Credential{
					Username: "admin",
					AuthType: sessions.AuthPassword,
					Password: "lab",
				}, nil
			}
			return sessiondial.Credential{}, errNoSuchCredential
		},
	}

	tp, err := sessiondial.Connect(context.Background(), n, opts)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer tp.Close()

	for _, ref := range asked {
		if ref == "a-credential-that-is-long-gone" {
			t.Fatal("a jump credential was resolved with no jump host in use")
		}
	}
}

// With a store present, Validate defers the username rule — so the gap has to
// be caught after the lookup, and the message has to say what is missing
// rather than surfacing as an authentication failure.
func TestNoUsernameAnywhereIsReportedPlainly(t *testing.T) {
	srv := startDevice(t, "lab-r1")

	n := nodeFor(srv)
	n.Username = ""
	n.Credential = ""

	opts := sessiondial.Options{
		Credentials: func(ref string) (sessiondial.Credential, error) {
			// A store, but one with no default in it.
			return sessiondial.Credential{}, nil
		},
	}

	_, err := sessiondial.Connect(context.Background(), n, opts)
	if err == nil {
		t.Fatal("connected with no username at all")
	}
	if !strings.Contains(err.Error(), "no username") {
		t.Fatalf("error does not name the gap: %v", err)
	}
}

// The only way to reach manual auth once a default exists: a session that says
// who it connects as has stated its own auth, and the default stays out.
func TestANodeThatNamesAUsernameOptsOutOfTheDefault(t *testing.T) {
	// A device that actually checks who is asking, so "it connected" proves
	// which username went over the wire. The permissive fake used by the
	// other tests here cannot tell the two apart.
	cfg := fakedev.IOS("lab-r1")
	cfg.Username = "admin"
	cfg.AcceptAnyPassword = true
	srv, err := fakedev.Start(cfg)
	if err != nil {
		t.Fatalf("start fake device: %v", err)
	}
	t.Cleanup(func() { srv.Close() })

	n := nodeFor(srv) // admin / lab, on the node itself
	n.Credential = ""

	opts := sessiondial.Options{
		Credentials: func(ref string) (sessiondial.Credential, error) {
			// A default that would connect as somebody else entirely.
			return sessiondial.Credential{
				Username: "not-admin",
				AuthType: sessions.AuthPassword,
				Password: "wrong",
			}, nil
		},
	}

	tp, err := sessiondial.Connect(context.Background(), n, opts)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer tp.Close()
	// The device only lets admin in, so getting here proves the node's own
	// username went over the wire rather than the default's.
	readUntil(t, tp, "lab-r1#", 5*time.Second)
}

// Normalize fills an empty AuthType with agent, so agent cannot be read as a
// choice. A node fresh from a map import has agent on it and has said nothing.
func TestAgentAuthAloneIsNotAStatement(t *testing.T) {
	srv := startDevice(t, "lab-r1")

	n := nodeFor(srv)
	n.Username = ""
	n.Password = ""
	n.AuthType = sessions.AuthAgent
	n.Credential = ""

	opts := sessiondial.Options{
		Credentials: func(ref string) (sessiondial.Credential, error) {
			return sessiondial.Credential{
				Username: "admin",
				AuthType: sessions.AuthPassword,
				Password: "lab",
			}, nil
		},
	}

	tp, err := sessiondial.Connect(context.Background(), n, opts)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer tp.Close()
	readUntil(t, tp, "lab-r1#", 5*time.Second)
}

type constError string

func (e constError) Error() string { return string(e) }

const errNoSuchCredential = constError("no such credential")

// Added Aug 13 2026 during the rebuild. The test above it
// (TestAStoreThatRefusesAnEmptyRefIsNotAnError) passes for the wrong reason:
// its node carries a username, so the empty ref is never looked up at all and
// swallowing that refusal is untested.
//
// With a node that states nothing, both behaviours refuse the connection --
// what the swallow buys is WHICH refusal. The store complaining about a
// reference the session never made says nothing anyone can act on; "no
// username" names the actual gap.
func TestAStoreRefusingAnEmptyRefStillReportsTheRealGap(t *testing.T) {
	srv := startDevice(t, "lab-r1")

	n := nodeFor(srv)
	n.Username = ""
	n.Password = ""
	n.AuthType = ""
	n.Credential = ""

	asked := 0
	opts := sessiondial.Options{
		Credentials: func(ref string) (sessiondial.Credential, error) {
			asked++
			return sessiondial.Credential{}, errNoSuchCredential
		},
	}

	_, err := sessiondial.Connect(context.Background(), n, opts)
	if err == nil {
		t.Fatal("connected with no username and no default")
	}
	if !strings.Contains(err.Error(), "no username") {
		t.Fatalf("error does not name the gap: %v", err)
	}
	if strings.Contains(err.Error(), "no such credential") {
		t.Fatalf("the store's complaint about an unnamed reference surfaced: %v", err)
	}
	if asked != 1 {
		t.Fatalf("store asked %d times, want exactly one empty-ref lookup", asked)
	}
}
