// internal/normalize/identity.go
//
// Canonical device identity.
//
// One device reached by address on one hop and by name on the next has to land
// on one identity, or every per-device cache in the system — credential
// bindings, jump bindings, the crawl claim set — warms twice and helps neither
// path. Nothing about that failure is loud. There is no error and no retry,
// just a cache that never hits and a crawl that walks the full credential
// ladder on every device forever.
//
// This is the only implementation. It used to exist three times: in the
// crawler's dial path, in the reach CLI, and in credres — and two of those
// three disagreed, which is the whole reason for the consolidation.
//
// # The CGNAT rule
//
// An address inside 100.64.0.0/10 (RFC 6598 shared address space) is not a
// stable identity. The same address is reused at every site, so two different
// devices collide on one key and inherit each other's credential bindings.
// Those get a reverse lookup and the PTR name becomes the identity.
//
// # Why the PTR is forward-confirmed
//
// A PTR record is not evidence that a name works. Stale reverse zones outlive
// the forward records they were written against, and a name that no longer
// resolves is worse than the address it replaced: dialing it fails where the
// address would have connected. So the PTR is looked up forward, and only a
// name that resolves back is trusted. Anything else keeps the address.
//
// The crawler has always done this; credres did not, and took the first PTR
// unconditionally. That disagreement is exactly the split-key bug described
// above, arriving only on devices whose reverse DNS is stale — which is to say
// arriving rarely, silently, and in production.
package normalize

import (
	"net"
	"net/netip"
	"strings"
)

// cgnatPrefix is the RFC 6598 shared address space.
var cgnatPrefix = netip.MustParsePrefix("100.64.0.0/10")

// Resolver is the DNS surface identity resolution needs. Swappable so the
// rules can be tested without a resolver, and so a caller can supply a cache.
type Resolver interface {
	LookupAddr(addr string) ([]string, error)
	LookupHost(host string) ([]string, error)
}

type netResolver struct{}

func (netResolver) LookupAddr(addr string) ([]string, error) { return net.LookupAddr(addr) }
func (netResolver) LookupHost(host string) ([]string, error) { return net.LookupHost(host) }

// DefaultResolver is the live one.
var DefaultResolver Resolver = netResolver{}

// NameResult is what Resolve decided and why. Callers log from this rather
// than re-deriving the reasoning, which is how the log line and the behavior
// stay in agreement.
type NameResult struct {
	// Name is the name or address to use: the confirmed PTR name when there
	// is one, otherwise the input unchanged.
	Name string

	// CGNAT reports whether the input was an address in 100.64.0.0/10. When
	// false, nothing was looked up and Name is the input.
	CGNAT bool

	// PTR is the reverse-lookup name that was found, confirmed or not. Empty
	// when the lookup failed or returned nothing.
	PTR string

	// Confirmed reports whether PTR resolved forward and was therefore
	// adopted as Name.
	Confirmed bool
}

// Resolve applies the CGNAT rule using the live resolver.
func Resolve(host string) NameResult { return ResolveWith(DefaultResolver, host) }

// ResolveWith applies the CGNAT rule: an address in 100.64.0.0/10 is reverse
// resolved, and the PTR name is adopted only if it resolves forward. Names and
// non-CGNAT addresses pass through untouched and cost no lookups.
func ResolveWith(r Resolver, host string) NameResult {
	h := strings.TrimSpace(host)
	res := NameResult{Name: h}

	addr, err := netip.ParseAddr(h)
	if err != nil || !cgnatPrefix.Contains(addr) {
		return res
	}
	res.CGNAT = true

	names, err := r.LookupAddr(h)
	if err != nil || len(names) == 0 {
		return res
	}
	ptr := strings.TrimSuffix(strings.TrimSpace(names[0]), ".")
	if ptr == "" {
		return res
	}
	res.PTR = ptr

	if fwd, err := r.LookupHost(ptr); err != nil || len(fwd) == 0 {
		// Stale reverse record. The address still works; the name does not.
		return res
	}
	res.Name, res.Confirmed = ptr, true
	return res
}

// Canonical is the identity of a device: the CGNAT rule applied, then any
// configured domain suffix stripped, so a box seen short and fully qualified
// is one device. Use this for cache keys; use Resolve().Name for the string to
// actually dial, which keeps its suffix because that is what resolves.
func Canonical(host string, suffixes []string) string {
	return CanonicalWith(DefaultResolver, host, suffixes)
}

// CanonicalWith is Canonical against a supplied resolver.
func CanonicalWith(r Resolver, host string, suffixes []string) string {
	if strings.TrimSpace(host) == "" {
		return ""
	}
	return StripSuffixes(ResolveWith(r, host).Name, suffixes)
}
