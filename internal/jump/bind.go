// internal/jump/bind.go
//
// The bridge between routing and secrets.
//
// This package resolves a device to a Path of named credentials and stops
// there. Turning those names into material is a separate step, kept separate
// on purpose: it is what lets the YAML be read, validated, and reviewed while
// the vault is locked, and it is the same seam the Python implementation uses
// (build_vault_credential_resolver adapts the vault to a callable that
// reachssh's ProxyResolver holds without ever seeing a secret).
//
// The lookup is by credential *name*, and it is expected to be cheap and
// cached by the caller: one lookup per bastion, not one per device.
package jump

import (
	"fmt"
	"io"
	"strings"
)

// Credential is the material needed to authenticate to one bastion. It is
// deliberately a small local type rather than the vault's record, so this
// package does not import the vault.
type Credential struct {
	Username      string
	Password      string
	KeyPath       string
	KeyPassphrase string
}

// CredentialLookup resolves a credential name to material. Returning an error
// is how "the config names a credential the vault does not have" surfaces.
type CredentialLookup func(name string) (Credential, error)

// BoundHop is a hop with its credential resolved.
type BoundHop struct {
	Hop
	Credential Credential
}

// BoundPath is a path ready to dial.
type BoundPath []BoundHop

// IsDirect reports whether the bound path has no hops.
func (p BoundPath) IsDirect() bool { return len(p) == 0 }

// Bind resolves every credential named in the path.
//
// A name the lookup cannot resolve is a hard error, not a fallback to direct:
// connecting straight at a device that is supposed to be behind a bastion is
// either going to fail confusingly or reach something it should not.
func Bind(p Path, lookup CredentialLookup) (BoundPath, error) {
	if len(p) == 0 {
		return nil, nil
	}
	if lookup == nil {
		return nil, fmt.Errorf("jump: path %s needs credentials but no lookup was provided", p)
	}
	out := make(BoundPath, 0, len(p))
	for _, h := range p {
		if strings.TrimSpace(h.Credential) == "" {
			return nil, fmt.Errorf("jump: hop %q has no credential name", h.Name)
		}
		c, err := lookup(h.Credential)
		if err != nil {
			return nil, fmt.Errorf(
				"jump: hop %q needs credential %q: %w (add it to the vault, or fix the 'credential:' name in the jump config)",
				h.Name, h.Credential, err)
		}
		out = append(out, BoundHop{Hop: h, Credential: c})
	}
	return out, nil
}

// CachedLookup wraps a lookup with a simple memo. Bastion credentials are
// looked up once per name per run rather than once per device.
func CachedLookup(lookup CredentialLookup) CredentialLookup {
	type entry struct {
		cred Credential
		err  error
	}
	cache := make(map[string]entry)
	return func(name string) (Credential, error) {
		if e, ok := cache[name]; ok {
			return e.cred, e.err
		}
		c, err := lookup(name)
		cache[name] = entry{cred: c, err: err}
		return c, err
	}
}

// Describe writes a human-readable summary of a decision. Credentials appear
// by name only; no material is ever written.
func Describe(w io.Writer, d Device, dec Decision) {
	name := d.Name
	if name == "" {
		name = d.Addr
	}
	rule := "default"
	if dec.RuleIndex >= 0 {
		rule = fmt.Sprintf("rule %d", dec.RuleIndex)
	}
	via := dec.Path.String()
	if dec.Inherited {
		via += " (inherited)"
	}
	fmt.Fprintf(w, "%s: %s via %s [target %s]\n", name, rule, via, dec.Target)
}
