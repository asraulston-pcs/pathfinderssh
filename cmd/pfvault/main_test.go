// cmd/pfvault/main_test.go
//
// The pure argument-shaping helpers. The command bodies themselves are
// vault I/O plus prompting, which the vault and vaultcli packages already
// cover; what is worth pinning here is the defaulting, because getting it
// wrong stores a credential whose declared auth type does not match the
// material it carries — and the dialer applies material strictly by declared
// type, so the mismatch shows up as an unexplained auth failure per device.
package main

import (
	"testing"

	"github.com/scottpeterman/pathfinderssh/internal/vault"
)

func TestInferAuthType(t *testing.T) {
	tests := []struct {
		name    string
		auth    string
		keyPath string
		want    string
	}{
		{"bare name and user means password", "", "", "password"},
		{"a key path implies publickey", "", "/home/u/.ssh/id_ed25519", "publickey"},
		{"an explicit auth type wins over the key path", "agent", "/home/u/.ssh/id_ed25519", "agent"},
		{"an explicit password type wins too", "password", "/home/u/.ssh/id_ed25519", "password"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := inferAuthType(tc.auth, tc.keyPath); got != tc.want {
				t.Errorf("inferAuthType(%q, %q) = %q, want %q", tc.auth, tc.keyPath, got, tc.want)
			}
		})
	}
}

func TestScopeSummary(t *testing.T) {
	tests := []struct {
		name  string
		scope vault.Scope
		want  string
	}{
		{"unrestricted", vault.Scope{}, "any"},
		{"domain", vault.Scope{DomainSuffix: "lab.example.net"}, "*.lab.example.net"},
		{"cidr", vault.Scope{CIDRs: []string{"10.20.0.0/16"}}, "10.20.0.0/16"},
		{"platform", vault.Scope{Platforms: []string{"arista_eos"}}, "arista_eos"},
		{
			name: "combined",
			scope: vault.Scope{
				DomainSuffix: "lab.example.net",
				CIDRs:        []string{"10.20.0.0/16"},
				Platforms:    []string{"arista_eos"},
			},
			want: "*.lab.example.net,10.20.0.0/16,arista_eos",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := scopeSummary(tc.scope); got != tc.want {
				t.Errorf("scopeSummary = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParentDir(t *testing.T) {
	tests := []struct{ in, want string }{
		{"/home/u/.pathfinder/vault.json", "/home/u/.pathfinder"},
		{"vault.json", ""},
		{"/vault.json", ""},
	}
	for _, tc := range tests {
		if got := parentDir(tc.in); got != tc.want {
			t.Errorf("parentDir(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
