// internal/vaultcli/vaultcli_test.go
package vaultcli

import "testing"

func TestBindingsPath(t *testing.T) {
	tests := []struct{ in, want string }{
		{"/home/u/.pathfinder/vault.json", "/home/u/.pathfinder/vault.bindings.json"},
		{"/home/u/.pathfinder/vault", "/home/u/.pathfinder/vault.bindings.json"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := BindingsPath(tc.in); got != tc.want {
			t.Errorf("BindingsPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
