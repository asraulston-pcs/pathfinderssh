// internal/sessiondial/connect_test.go
//
// These run against internal/fakedev — a real SSH server in-process — so
// "Connect returns a usable transport" is proven rather than asserted. No
// display, no lab, no network beyond loopback.
package sessiondial_test

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/scottpeterman/pathfinderssh/internal/fakedev"
	"github.com/scottpeterman/pathfinderssh/internal/sessiondial"
	"github.com/scottpeterman/pathfinderssh/internal/sessions"
	"github.com/scottpeterman/pathfinderssh/internal/term"
)

func startDevice(t *testing.T, name string) *fakedev.Server {
	t.Helper()
	srv, err := fakedev.Start(fakedev.IOS(name))
	if err != nil {
		t.Fatalf("start fake device: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv
}

// nodeFor builds a node pointed at the fake device. Host-key verification is
// off because the device generates a fresh key per run; that is the one case
// the insecure policy exists for.
func nodeFor(srv *fakedev.Server) sessions.Node {
	n := sessions.Defaults()
	n.Name = "lab-r1"
	n.Host = srv.Host()
	n.Port = srv.Port()
	n.Username = "admin"
	n.AuthType = sessions.AuthPassword
	n.Password = "lab"
	n.HostKeyPolicy = sessions.HostKeyInsecure
	return n
}

// readUntil reads from the transport until want appears or the deadline
// passes. A blocking Read with no bound is how a failing test becomes a
// hanging one.
func readUntil(t *testing.T, tp term.Transport, want string, within time.Duration) string {
	t.Helper()
	got := make(chan string, 1)
	go func() {
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := tp.Read(buf)
			if n > 0 {
				sb.Write(buf[:n])
				if strings.Contains(sb.String(), want) {
					got <- sb.String()
					return
				}
			}
			if err != nil {
				got <- sb.String()
				return
			}
		}
	}()
	select {
	case s := <-got:
		return s
	case <-time.After(within):
		t.Fatalf("timed out waiting for %q", want)
		return ""
	}
}

func TestConnectSSHReturnsALiveTransport(t *testing.T) {
	srv := startDevice(t, "lab-r1")

	tp, err := sessiondial.Connect(context.Background(), nodeFor(srv), sessiondial.Options{})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer tp.Close()

	if out := readUntil(t, tp, "lab-r1#", 5*time.Second); !strings.Contains(out, "lab-r1#") {
		t.Fatalf("no prompt in %q", out)
	}
	if _, err := tp.Write([]byte("show version\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if out := readUntil(t, tp, "15.6(2)T", 5*time.Second); !strings.Contains(out, "15.6(2)T") {
		t.Fatalf("command output not returned: %q", out)
	}
	if err := tp.Resize(term.Size{Cols: 132, Rows: 40}); err != nil {
		t.Fatalf("resize: %v", err)
	}
}

func TestCloseIsReportedAsACleanEnd(t *testing.T) {
	srv := startDevice(t, "lab-r1")
	tp, err := sessiondial.Connect(context.Background(), nodeFor(srv), sessiondial.Options{})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	readUntil(t, tp, "lab-r1#", 5*time.Second)

	if err := tp.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case <-tp.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done never closed after a local Close")
	}
	if err := tp.Err(); err != nil {
		t.Fatalf("a local close should report nil, got %v", err)
	}
}

func TestCredentialLookupSuppliesTheSecrets(t *testing.T) {
	srv := startDevice(t, "lab-r1")

	n := nodeFor(srv)
	// Nothing on the node itself: the vault reference has to carry it.
	n.Username = ""
	n.Password = ""
	n.AuthType = ""
	n.Credential = "lab-readonly"

	var asked string
	opts := sessiondial.Options{
		Credentials: func(ref string) (sessiondial.Credential, error) {
			asked = ref
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
	if asked != "lab-readonly" {
		t.Fatalf("lookup asked for %q", asked)
	}
	readUntil(t, tp, "lab-r1#", 5*time.Second)
}

func TestReferencedCredentialWithNoStoreIsAnError(t *testing.T) {
	srv := startDevice(t, "lab-r1")
	n := nodeFor(srv)
	n.Credential = "lab-readonly"

	// No Credentials lookup. It must say so rather than quietly dialing
	// with whatever happened to be on the node.
	_, err := sessiondial.Connect(context.Background(), n, sessiondial.Options{})
	if err == nil {
		t.Fatal("expected an error when a credential is referenced and no store exists")
	}
	if !strings.Contains(err.Error(), "lab-readonly") {
		t.Fatalf("error does not name the credential: %v", err)
	}
}

func TestTOFUWithNoPromptRefusesAnUnknownHost(t *testing.T) {
	srv := startDevice(t, "lab-r1")
	n := nodeFor(srv)
	n.HostKeyPolicy = sessions.HostKeyTOFU
	n.KnownHostsPath = t.TempDir() + "/known_hosts"

	// Nobody to ask means the answer is no. "No callback" resolving to
	// "accept" would make the policy meaningless in exactly the headless
	// context where it matters most.
	if _, err := sessiondial.Connect(context.Background(), n, sessiondial.Options{}); err == nil {
		t.Fatal("TOFU with no prompt accepted an unknown host key")
	}
}

func TestTOFUPromptAcceptanceIsReported(t *testing.T) {
	srv := startDevice(t, "lab-r1")
	n := nodeFor(srv)
	n.HostKeyPolicy = sessions.HostKeyTOFU
	n.KnownHostsPath = t.TempDir() + "/known_hosts"

	var seenHost, seenFingerprint string
	opts := sessiondial.Options{
		HostKeyPrompt: func(hostname string, remote net.Addr, key ssh.PublicKey) (bool, error) {
			return true, nil
		},
		OnNewHostKey: func(host, keyType, fingerprint string) {
			seenHost, seenFingerprint = host, fingerprint
		},
	}
	tp, err := sessiondial.Connect(context.Background(), n, opts)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer tp.Close()

	if seenHost == "" || seenFingerprint == "" {
		t.Fatal("first contact was not reported; trust on first use is blind")
	}
	if !strings.HasPrefix(seenFingerprint, "SHA256:") {
		t.Fatalf("fingerprint = %q, want a SHA256 form", seenFingerprint)
	}
}

func TestInvalidNodeFailsBeforeAnythingIsDialed(t *testing.T) {
	n := sessions.Defaults()
	n.Host = "" // required for ssh
	_, err := sessiondial.Connect(context.Background(), n, sessiondial.Options{})
	if err == nil {
		t.Fatal("expected a validation error")
	}
	if !strings.Contains(err.Error(), "host") {
		t.Fatalf("error should name the field: %v", err)
	}
}

func TestCancelledContextIsHonouredBeforeDialing(t *testing.T) {
	srv := startDevice(t, "lab-r1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := sessiondial.Connect(ctx, nodeFor(srv), sessiondial.Options{}); err == nil {
		t.Fatal("a cancelled context should stop the dial")
	}
}

func TestResolveCGNATLeavesOrdinaryTargetsAlone(t *testing.T) {
	for _, host := range []string{"lab-r1.lab.example", "172.16.1.2", "10.0.0.108", "192.0.2.7"} {
		if got := sessiondial.ResolveCGNAT(host, nil); got != host {
			t.Errorf("ResolveCGNAT(%q) = %q, want it untouched", host, got)
		}
	}
}

func TestResolveCGNATNeverFailsOnAnUnresolvableAddress(t *testing.T) {
	// In 100.64.0.0/10 with nothing behind it. The address is used as
	// given; a lookup miss must never be the reason a session does not
	// open.
	const addr = "100.64.201.203"
	if got := sessiondial.ResolveCGNAT(addr, nil); got != addr {
		t.Fatalf("ResolveCGNAT(%q) = %q", addr, got)
	}
}
