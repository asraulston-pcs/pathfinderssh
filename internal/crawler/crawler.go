// internal/crawler/crawler.go
// Depth-batched BFS network crawler: dial -> fingerprint -> run the
// platform's neighbor plan -> parse -> claim and enqueue neighbors for the
// next depth. Concurrency is per depth level (worker pool), matching the
// Python engine's shape. The caller supplies a DialFunc so all auth/jump/
// host-key policy stays in the CLI layer; the crawler passes it a DialTarget
// describing which device it means, and learns nothing about how the
// connection was made.
package crawler

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"

	"github.com/scottpeterman/pathfinderssh/internal/crawlrun"
	"github.com/scottpeterman/pathfinderssh/internal/dial"
	"github.com/scottpeterman/pathfinderssh/internal/netexec"
	"github.com/scottpeterman/pathfinderssh/internal/normalize"
	"github.com/scottpeterman/pathfinderssh/internal/topo"
)

// DialTarget and DialFunc moved to internal/dial when capture became the
// second consumer of the dial layer. Aliases rather than a rename: every
// call site in the crawler and its tests still reads DialTarget, which is
// the right word here, and the type is genuinely the same one capture uses.
type DialTarget = dial.Target

// DialFunc opens an SSH connection to a target. See dial.Func.
type DialFunc = dial.Func

// Logf receives progress lines; nil discards them.
type Logf func(format string, args ...any)

type Config struct {
	Dial        DialFunc
	MaxDepth    int // 0 = seeds only
	Concurrency int // workers per depth batch (default 5)
	// Domains are suffixes appended when a neighbor name does not resolve
	// as reported ("eng-spine-1" -> "eng-spine-1.<domain>"; first suffix that
	// resolves wins). The same list is stripped from identities for crawl
	// dedup, so a device claimed as "eng-spine-1" and dialed as
	// "eng-spine-1.<domain>" is one claim.
	Domains         []string
	ExcludePatterns []string // substring match vs platform/hostname/sysname
	// AllowDomains restricts which neighbors are DIALED. When non-empty,
	// only neighbor names suffix-matching an entry are enqueued; everything
	// else (including bare-IP fallback targets) is kept in the map as a
	// leaf but never connected to. Essential when seeds face an IX or any
	// shared fabric where LLDP sees third-party devices.
	AllowDomains []string
	// DisableIPFallback turns off retrying a failed dial against the
	// management address the neighbor reported. The zero value keeps the
	// fallback ON: a neighbor that told us both a name and an address has
	// given us two chances to reach it, and declining to use the second
	// one because the first did not resolve is throwing away information
	// the device handed us.
	DisableIPFallback bool
	SessionOpts       netexec.Options
	Log               Logf

	// Emit receives the same events as structured values, for anything that
	// needs to accumulate state rather than watch output scroll past. It is
	// additive: every emit sits beside the Log call it mirrors, so adding a UI
	// can never change what the CLI prints. A nil Emit costs nothing.
	Emit crawlrun.Emit
}

type Crawler struct {
	cfg      Config
	resolver normalize.Resolver
	mu       sync.Mutex
	claimed  map[string]struct{}
	devices  []*topo.Device
}

// dialAllowed applies the AllowDomains policy to a candidate target.
// A name passes when it suffix-matches an allowed domain as reported, OR
// when appending one of the resolution Domains (a) lands it under an
// allowed domain and (b) the completed name actually resolves — the DNS
// requirement is what stops third-party FQDNs from qualifying via a
// bogus appended suffix.
func (c *Crawler) dialAllowed(target string) bool {
	if len(c.cfg.AllowDomains) == 0 {
		return true
	}
	if _, err := netip.ParseAddr(target); err == nil {
		// bare-IP fallback target: no name to match the allowlist against,
		// so with an allowlist active it is not dialed.
		return false
	}
	matches := func(name string) bool {
		n := normalize.Identifier(name)
		for _, d := range c.cfg.AllowDomains {
			d = normalize.Identifier(strings.TrimPrefix(strings.TrimSpace(d), "."))
			if d == "" {
				continue
			}
			if n == d || strings.HasSuffix(n, "."+d) {
				return true
			}
		}
		return false
	}
	if matches(target) {
		return true
	}
	for _, d := range c.cfg.Domains {
		d = strings.TrimPrefix(strings.TrimSpace(d), ".")
		if d == "" {
			continue
		}
		cand := target + "." + d
		if !matches(cand) {
			continue
		}
		if _, err := c.resolver.LookupHost(cand); err == nil {
			return true
		}
	}
	return false
}

func New(cfg Config) *Crawler {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 5
	}
	if cfg.Log == nil {
		cfg.Log = func(string, ...any) {}
	}
	return &Crawler{
		cfg:      cfg,
		resolver: normalize.DefaultResolver,
		claimed:  map[string]struct{}{},
	}
}

// identity is the crawler's key for a device: the target with any configured
// domain suffix stripped, so a box seen short and fully qualified is one
// device. This is the value handed to the dial layer as DialTarget.Identity,
// and it must stay the same function the claim set is keyed on.
func (c *Crawler) identity(target string) string {
	return normalize.StripSuffixes(target, c.cfg.Domains)
}

// tryClaim registers a target (and its short form) exactly once.
func (c *Crawler) tryClaim(target string) bool {
	id := c.identity(target)
	short := normalize.ShortName(target)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.claimed[id]; ok {
		return false
	}
	if _, ok := c.claimed[short]; ok {
		return false
	}
	c.claimed[id] = struct{}{}
	c.claimed[short] = struct{}{}
	return true
}

// registerAliases claims a discovered device's other identities so the same
// box reached by a different name later is not re-crawled.
func (c *Crawler) registerAliases(d *topo.Device) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range []string{d.Hostname, d.SysName, d.IPAddress} {
		if id != "" {
			c.claimed[normalize.StripSuffixes(id, c.cfg.Domains)] = struct{}{}
			c.claimed[normalize.ShortName(id)] = struct{}{}
		}
	}
}

// resolveName applies the CGNAT rule and logs what it decided. The rule
// itself lives in normalize because credres has to reach the same answer:
// the crawler's claim key and the binding cache key are the same string, or
// the cache is useless.
func (c *Crawler) resolveName(target string) string {
	res := normalize.ResolveWith(c.resolver, target)
	switch {
	case !res.CGNAT:
	case res.PTR == "":
		c.cfg.Log("crawl: %s is CGNAT (100.64/10) with no PTR; using address", target)
	case res.Confirmed:
		c.cfg.Log("crawl: %s is CGNAT (100.64/10) -> %s; using name", target, res.PTR)
		c.cfg.Emit.Send(crawlrun.Event{Kind: crawlrun.KindResolved, Identity: target,
			Detail: "CGNAT -> " + res.PTR})
	default:
		c.cfg.Log("crawl: %s is CGNAT (100.64/10) -> %s but that name does not "+
			"resolve; using address", target, res.PTR)
	}
	return res.Name
}

// resolveViaDomains appends configured domain suffixes when the target
// does not resolve as reported; the first candidate with an A/AAAA record
// wins. Targets that already resolve (or are IPs) pass through.
func (c *Crawler) resolveViaDomains(target string) string {
	if len(c.cfg.Domains) == 0 {
		return target
	}
	if _, err := netip.ParseAddr(target); err == nil {
		return target
	}
	if _, err := c.resolver.LookupHost(target); err == nil {
		return target
	}
	for _, d := range c.cfg.Domains {
		d = strings.TrimPrefix(strings.TrimSpace(d), ".")
		if d == "" {
			continue
		}
		cand := target + "." + d
		if _, err := c.resolver.LookupHost(cand); err == nil {
			c.cfg.Log("crawl: resolved %s via domain suffix -> %s", target, cand)
			c.cfg.Emit.Send(crawlrun.Event{Kind: crawlrun.KindResolved, Identity: target,
				Detail: "domain suffix -> " + cand})
			return cand
		}
	}
	return target
}

// nextTarget decides what to enqueue for a neighbor claim: prefer the
// reported name (with domain resolution left to DNS at dial time); fall
// back to the management IP when the "name" is really a chassis MAC.
//
// The second return is the management address the neighbor reported, kept
// alongside the name rather than discarded. It used to be read only in the
// chassis-MAC branch, which meant a device whose name simply did not resolve
// was a dead end even though the claim carried a working address. That is a
// common shape in a lab with no DNS, and it is not rare in production either
// — a device renamed after its A record was written looks identical.
func nextTarget(n topo.Neighbor, log Logf) (string, string, bool) {
	name := strings.TrimSpace(n.RemoteDevice)
	addr := strings.TrimSpace(n.RemoteIP)
	if normalize.IsMACAddress(addr) {
		addr = ""
	}
	if normalize.IsArtifactName(name) {
		return "", "", false
	}
	if normalize.IsMACAddress(name) {
		if addr != "" {
			log("crawl: neighbor named by chassis MAC %s; queuing by IP %s", name, addr)
			return addr, addr, true
		}
		log("crawl: neighbor %s is a chassis MAC with no usable IP; skipping", name)
		return "", "", false
	}
	return name, addr, true
}

// shouldRetryByAddr decides whether a failed dial is worth one more attempt
// against the management address the neighbor reported.
//
// Only reachability failures qualify. An authentication rejection must never
// come back here: the dial layer walks a credential ladder, so retrying at a
// second address would spend the whole ladder twice against one account and
// double the lockout exposure this subsystem exists to bound. net.Error and
// *net.OpError cover DNS failures, refused connections and timeouts; an SSH
// auth failure is none of those, which is what makes the discriminator hold
// without the crawler having to know anything about credentials.
func (c *Crawler) shouldRetryByAddr(dt DialTarget, err error) bool {
	if c.cfg.DisableIPFallback || dt.Addr == "" || dt.Addr == dt.Target {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr)
}

// mergeNeighbor fills gaps in an already-recorded edge from a later step that
// describes the same link.
//
// Two commands routinely describe one link at different resolutions: a summary
// form that knows only names and ports, and a detail form that also carries a
// management address, a platform string and a port description. Whichever runs
// first used to win outright, so a plan that happened to list the summary first
// produced address-less edges while the address sat parsed and discarded one
// step later. Plan order still decides the PRIMARY record; this makes the
// order stop being the difference between having a management address and not.
//
// Only empty fields are filled. A later step never overwrites a value an
// earlier one supplied — the first record is still authoritative for anything
// it actually knows, which keeps this from quietly rewriting a good name with
// a truncated one.
func mergeNeighbor(dst *topo.Neighbor, src topo.Neighbor) {
	fill := func(d *string, s string) {
		if *d == "" {
			*d = strings.TrimSpace(s)
		}
	}
	fill(&dst.RemoteIP, src.RemoteIP)
	fill(&dst.RemotePlatform, src.RemotePlatform)
	fill(&dst.RemoteDescr, src.RemoteDescr)
	fill(&dst.Capabilities, src.Capabilities)
	fill(&dst.RemoteInterface, src.RemoteInterface)
}

// crawlOne runs the full per-device pipeline against an already-admitted
// device: resolution and claiming happened in admit, so this only dials.
func (c *Crawler) crawlOne(ctx context.Context, it item) *topo.Device {
	target := it.target
	d := &topo.Device{Hostname: target, Depth: it.depth}

	dt := DialTarget{
		Target:   it.target,
		Reported: it.reported,
		Identity: it.identity,
		Addr:     it.addr,
		Depth:    it.depth,
	}
	if _, err := netip.ParseAddr(it.target); err == nil {
		dt.Addr = it.target
	}
	client, err := c.cfg.Dial(ctx, dt)
	if err != nil && c.shouldRetryByAddr(dt, err) {
		c.cfg.Emit.Send(crawlrun.Event{Kind: crawlrun.KindRetryAddr,
			Identity: it.identity, Detail: it.addr})
		c.cfg.Log("crawl: %s unreachable by name (%v); retrying at reported address %s",
			it.target, err, dt.Addr)
		byAddr := dt
		byAddr.Target = dt.Addr
		// Identity deliberately does NOT change. The device is the same
		// device; only the route to it is. Re-keying on the address here
		// would split every cache downstream — a binding written under the
		// address would never be found again by a caller holding the name.
		client, err = c.cfg.Dial(ctx, byAddr)
	}
	if err != nil {
		d.Failed, d.FailedWhy = true, fmt.Sprintf("dial: %v", err)
		return d
	}
	defer client.Close()
	// Record the address the device actually answered on, not the string
	// that was dialed. registerAliases claims this, so a device reached by
	// name here and by address from a neighbor's claim later is recognized
	// as one device instead of being crawled and mapped twice.
	if host, _, err := net.SplitHostPort(client.RemoteAddr()); err == nil {
		d.IPAddress = host
	} else if host, _, err := net.SplitHostPort(client.Addr()); err == nil {
		d.IPAddress = host
	}

	sess, err := netexec.Open(ctx, client, c.cfg.SessionOpts)
	if err != nil {
		d.Failed, d.FailedWhy = true, fmt.Sprintf("session: %v", err)
		return d
	}
	defer sess.Close()

	fp, err := netexec.Fingerprint(ctx, sess)
	if err != nil || fp == nil {
		d.Failed, d.FailedWhy = true, fmt.Sprintf("fingerprint: %v", err)
		return d
	}
	d.Platform = fp.Name
	d.Version = fp.VersionOutput

	// The device's own name, out of its prompt. Always recorded as SysName
	// so the claim set and the topology pass can alias on it. Promoted to
	// Hostname only when the device was reached by address, because that is
	// the case where the alternative is a map node labelled 10.0.0.1.
	if sys := normalize.HostnameFromPrompt(sess.Prompt()); sys != "" {
		d.SysName = sys
		if _, err := netip.ParseAddr(target); err == nil {
			// Only when the name actually differs from what it was claimed
			// under. "x identifies itself as x" is not a decision, and a
			// decisions list that fills with no-ops stops being read.
			if !strings.EqualFold(normalize.Canonical(sys, c.cfg.Domains), it.identity) {
				c.cfg.Emit.Send(crawlrun.Event{Kind: crawlrun.KindRenamed,
					Identity: it.identity, Name: sys})
			}
			c.cfg.Log("crawl: %s identifies itself as %q; naming the node from its prompt",
				target, sys)
			d.Hostname = sys
		}
	}

	plan, ok := planFor(fp.Name)
	if !ok {
		// discovered but not crawlable (e.g. linux, unknown): keep as a
		// mapped leaf with no neighbors.
		c.cfg.Emit.Send(crawlrun.Event{Kind: crawlrun.KindPlatform,
			Identity: it.identity, Platform: fp.Name, Detail: "no neighbor plan; leaf"})
		c.cfg.Log("crawl: %s platform %q has no neighbor plan; leaf", target, fp.Name)
		return d
	}

	// edge dedup within the device: (local_if, peer, remote_if)
	// seen maps an edge key to its index in d.Neighbors, so a later step
	// describing the same link can ENRICH the record rather than being
	// dropped. See mergeNeighbor.
	seen := map[[3]string]int{}
	for _, st := range plan {
		out, err := sess.Run(ctx, st.Command)
		if err != nil {
			if st.BestEffort {
				c.cfg.Emit.Send(crawlrun.Event{Kind: crawlrun.KindCollectErr,
					Identity: it.identity, Detail: st.Command + ": " + err.Error()})
				c.cfg.Log("crawl: %s: %q failed (best-effort): %v", target, st.Command, err)
				continue
			}
			d.Failed, d.FailedWhy = true, fmt.Sprintf("%q: %v", st.Command, err)
			return d
		}
		recs, err := parseStep(fp.Name, st, out)
		if err != nil {
			if st.BestEffort {
				c.cfg.Log("crawl: %s: parse %q (best-effort): %v", target, st.Command, err)
				continue
			}
			c.cfg.Log("crawl: %s: parse %q: %v", target, st.Command, err)
			continue
		}
		// Per-step accounting. A step that runs, parses cleanly and still
		// contributes nothing used to be completely silent — the crawl
		// reported success while running on whatever the other steps
		// happened to supply. That silence cost real debugging time: a
		// plan whose first step carries no management address looks
		// identical to a device that never advertised one.
		var added, enriched, skipped int
		for _, n := range recs {
			if !st.EdgeSource && n.LocalInterface == "" {
				// enrichment-only record (e.g. IOS lldp detail without
				// Local Intf) — merge fields into an existing edge later;
				// for now it cannot create an edge on its own.
				skipped++
				continue
			}
			key := [3]string{
				normalize.Interface(n.LocalInterface),
				normalize.Identifier(n.RemoteDevice),
				normalize.Interface(n.RemoteInterface),
			}
			if idx, dup := seen[key]; dup {
				mergeNeighbor(&d.Neighbors[idx], n)
				enriched++
				continue
			}
			seen[key] = len(d.Neighbors)
			d.Neighbors = append(d.Neighbors, n)
			added++
		}
		c.cfg.Emit.Send(crawlrun.Event{Kind: crawlrun.KindCollect, Identity: it.identity,
			Detail: st.Command, Parsed: len(recs), New: added,
			Enriched: enriched, Skipped: skipped})
		c.cfg.Log("crawl: %s: %q -> %d parsed, %d new, %d enriched, %d skipped",
			target, st.Command, len(recs), added, enriched, skipped)
	}
	return d
}

// item is one admitted device: resolved, claimed, and ready to dial.
type item struct {
	target   string // resolved: what to dial
	reported string // as claimed by a neighbor, before resolution
	identity string // the claim key
	addr     string // management address from the neighbor claim, if any
	depth    int

	// parent is the identity of the device whose neighbor table produced
	// this one. Empty for a seed.
	//
	// This is the answer to the first question anyone asks about an
	// unexpected row — "where did that come from" — and without it the only
	// way to find out is to correlate log lines by hand. It is also the BFS
	// parentage the jump-host wiring needs: a device discovered behind a
	// bastion is reachable the way its parent was reachable.
	parent string
}

// admit resolves a reported target to the string that will actually be dialed,
// derives the identity from THAT, and claims it.
//
// The order matters and used to be wrong. Resolution ran inside crawlOne,
// after the claim, so a CGNAT address whose PTR resolved was claimed under the
// address and dialed by name — two keys for one device the moment anything
// downstream started caching on identity. Resolve, then claim, then dial.
func (c *Crawler) admit(reported, addr string, depth int, parent string) (item, bool) {
	target := c.resolveViaDomains(c.resolveName(reported))
	if !c.tryClaim(target) {
		return item{}, false
	}
	return item{
		target:   target,
		reported: reported,
		identity: c.identity(target),
		addr:     addr,
		depth:    depth,
		parent:   parent,
	}, true
}

// Crawl runs the BFS from the seeds and returns all crawled devices.
// Crawl runs to completion. Equivalent to CrawlContext with a background
// context; kept so the CLI, which has signal handling of its own, is
// unchanged.
func (c *Crawler) Crawl(seeds []string) []*topo.Device {
	return c.CrawlContext(context.Background(), seeds)
}

// CrawlContext runs until the frontier empties or ctx is cancelled.
//
// Cancellation is checked in two places: between depth batches, and inside
// each worker once it holds a slot. The second check is what makes a stop
// feel immediate — every device in a batch spawns a goroutine straight away
// and then blocks on the semaphore, so on cancel the queued ones fall through
// without dialing and only the handful actually in flight have to drain.
//
// Devices abandoned this way are returned marked failed with a reason rather
// than dropped. A device that silently vanishes from a stopped run is
// indistinguishable from one that was never discovered, and the whole point of
// reporting a crawl is that the two are not the same.
func (c *Crawler) CrawlContext(ctx context.Context, seeds []string) []*topo.Device {
	if ctx == nil {
		ctx = context.Background()
	}
	var batch []item
	for _, s := range seeds {
		if it, ok := c.admit(s, "", 0, ""); ok {
			batch = append(batch, it)
		}
	}

	for len(batch) > 0 {
		if err := ctx.Err(); err != nil {
			c.cfg.Log("crawl: stopped before depth %d with %d device(s) pending",
				batch[0].depth, len(batch))
			c.recordCancelled(batch, err)
			break
		}
		depth := batch[0].depth
		c.cfg.Log("crawl: depth %d, %d device(s)", depth, len(batch))
		c.cfg.Emit.Send(crawlrun.Event{Kind: crawlrun.KindDepth, Depth: depth})
		for _, it := range batch {
			c.cfg.Emit.Send(crawlrun.Event{
				Kind: crawlrun.KindQueued, Identity: it.identity,
				Depth: it.depth, Via: it.parent,
			})
		}

		results := make([]*topo.Device, len(batch))
		sem := make(chan struct{}, c.cfg.Concurrency)
		var wg sync.WaitGroup
		for i, it := range batch {
			wg.Add(1)
			go func(i int, it item) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				if err := ctx.Err(); err != nil {
					results[i] = cancelledDevice(it, err)
					return
				}
				results[i] = c.crawlOne(ctx, it)
			}(i, it)
		}
		wg.Wait()

		// Two passes, and the order matters. Every device in this batch has
		// to register its own names BEFORE any neighbor list is walked,
		// because a batch routinely contains a device that another device in
		// the same batch also reports as a neighbor. Registering and admitting
		// in one pass means the earlier device's neighbors are admitted while
		// the later device has not yet claimed the names it answers to — so it
		// is claimed a second time, dialed a second time, and spends a second
		// set of credential attempts on an account that already worked.
		c.claimAll(results)

		var next []item
		for i, d := range results {
			// Events key on the claim identity, never on Hostname. Hostname
			// is the string dialed; identity is what the device was claimed
			// under, and with a domain suffix configured the two differ — so
			// keying terminal events on Hostname files them against a second
			// row and leaves the first one looking unfinished.
			identity := batch[i].identity

			if d.Failed {
				c.cfg.Emit.Send(crawlrun.Event{Kind: crawlrun.KindFailed,
					Identity: identity, Name: d.SysName, Detail: d.FailedWhy})
				c.cfg.Log("crawl: %s FAILED: %s", d.Hostname, d.FailedWhy)
				continue
			}
			// Success has no log line of its own — the crawler only narrates
			// what went wrong — so this is the one emit with no cfg.Log beside
			// it. Without it every device that worked stays mid-flight and
			// Finish sweeps it into a failure, which is the opposite of what
			// happened.
			c.cfg.Emit.Send(crawlrun.Event{Kind: crawlrun.KindReached,
				Identity: identity, Name: d.SysName, Platform: d.Platform})
			if excl, pat := normalize.ShouldExclude(
				[]string{d.Platform, d.Hostname, d.SysName},
				c.cfg.ExcludePatterns); excl {
				c.cfg.Log("crawl: %s excluded from propagation (pattern %q)", d.Hostname, pat)
				continue
			}
			if depth >= c.cfg.MaxDepth {
				continue
			}
			for _, n := range d.Neighbors {
				t, addr, ok := nextTarget(n, c.cfg.Log)
				if !ok {
					continue
				}
				// Pre-dial exclusion: the LLDP/CDP claim already carries
				// the remote description/platform — skip dialing anything
				// matching the exclude patterns. It stays in the map as a
				// leaf; we just never connect to it.
				if excl, pat := normalize.ShouldExclude(
					[]string{n.RemoteDescr, n.RemotePlatform, n.RemoteDevice},
					c.cfg.ExcludePatterns); excl {
					c.cfg.Emit.Send(crawlrun.Event{Kind: crawlrun.KindNotDialed,
						Identity: t, Via: identity, Depth: depth + 1,
						Detail: "matches exclude " + pat})
					c.cfg.Log("crawl: %s matches exclude %q (from neighbor claim); mapped as leaf, not dialed", t, pat)
					continue
				}
				if !c.dialAllowed(t) {
					c.cfg.Emit.Send(crawlrun.Event{Kind: crawlrun.KindNotDialed,
						Identity: t, Via: identity, Depth: depth + 1,
						Detail: "outside allowed domains"})
					c.cfg.Log("crawl: %s outside allowed domains; mapped as leaf, not dialed", t)
					continue
				}
				if n, ok := c.admit(t, addr, depth+1, identity); ok {
					next = append(next, n)
				}
			}
		}
		batch = next
	}
	return c.devices
}

// parseStep is separated for testability against captured output.
func parseStep(platform string, st step, output string) ([]topo.Neighbor, error) {
	recs, err := tfsmParse(platform, st.Key, output)
	if err != nil {
		return nil, err
	}
	out := make([]topo.Neighbor, 0, len(recs))
	for _, r := range recs {
		out = append(out, recordToNeighbor(r, st.Protocol))
	}
	return out, nil
}

// claimAll registers every device in a finished batch and files it, before any
// neighbor list from that batch is walked. See the comment at the call site:
// splitting this out of the admission loop is the whole point.
func (c *Crawler) claimAll(results []*topo.Device) {
	for _, d := range results {
		if d == nil {
			continue
		}
		c.registerAliases(d)
	}
	c.mu.Lock()
	for _, d := range results {
		if d != nil {
			c.devices = append(c.devices, d)
		}
	}
	c.mu.Unlock()
}

// cancelledDevice is the record for a device the crawl gave up on because it
// was stopped, as distinct from one that was tried and failed.
func cancelledDevice(it item, err error) *topo.Device {
	return &topo.Device{
		Hostname:  it.target,
		Depth:     it.depth,
		Failed:    true,
		FailedWhy: "crawl stopped before this device was attempted: " + err.Error(),
	}
}

// recordCancelled files everything still queued when a crawl is stopped, so
// the run's device count still accounts for the whole frontier.
func (c *Crawler) recordCancelled(batch []item, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, it := range batch {
		c.devices = append(c.devices, cancelledDevice(it, err))
		c.cfg.Emit.Send(crawlrun.Event{Kind: crawlrun.KindFailed, Identity: it.identity,
			Detail: "crawl stopped before this device was attempted"})
	}
}
