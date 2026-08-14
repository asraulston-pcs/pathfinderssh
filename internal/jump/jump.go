// internal/jump/jump.go
//
// Rule-based bastion routing: turning "I am about to dial this device" into
// the ordered list of jump hosts to tunnel through.
//
// This is the sibling of internal/credres. credres answers *who am I* when
// dialing; jump answers *how do I get there*. They are deliberately separate
// packages with separate on-disk formats, because they are not the same kind
// of information:
//
//   - Routing policy is not secret. It wants to be readable while the vault is
//     locked, diffable, reviewable, and shareable across a team. It lives in
//     plain YAML.
//   - Routing policy is route-map ordered. First match wins and position
//     expresses the exception, because the operator knows their bastions and
//     wants explicit control. Credential laddering is specificity-ordered and
//     ranks automatically, because discovery keeps finding devices nobody
//     declared.
//
// Folding one into the other would force one of those two orderings onto a
// problem it does not fit.
//
// The two meet at a credential *name*: a jump host names a vault credential,
// and resolving that name is somebody else's job. This package never holds
// secret material. See bind.go for the bridge.
//
// The model follows the Python implementation in secure-cartography
// (sc2/scng/discovery/jump.py), with three changes noted at their definitions:
// the site/role match keys are dropped, CIDR matching is added, and "inherit"
// is added as a jump target.
package jump

import (
	"errors"
	"fmt"
	"net/netip"
	"path"
	"regexp"
	"strings"
)

// Reserved jump targets, usable anywhere a host name is expected in a rule.
const (
	// TargetDirect connects without a bastion.
	TargetDirect = "direct"

	// TargetInherit reuses the path that reached the device this one was
	// learned from. Not in the Python original, which never needed it: SC2
	// rules are written against a known device list, while a crawl reaches
	// devices nobody declared. If a spine was reached through a bastion, its
	// neighbors are almost certainly behind the same bastion, so this lets the
	// config carry rules for seeds and exceptions instead of the whole fabric.
	//
	// With no inherited path available, it falls through to direct.
	TargetInherit = "inherit"
)

// Host is a bastion definition.
type Host struct {
	// Name is the key this host is referenced by in rules. Populated from the
	// YAML mapping key at load time.
	Name string `yaml:"-"`

	// Host is the address or hostname of the bastion.
	Host string `yaml:"host"`

	// Port defaults to 22 when unset.
	Port int `yaml:"port"`

	// Credential names a vault credential. Resolution happens elsewhere; this
	// package only carries the name.
	Credential string `yaml:"credential"`

	// Via chains this bastion behind another, for multi-hop paths. Empty means
	// the bastion is reachable directly.
	Via string `yaml:"via"`
}

// Match is the predicate on a rule. Every populated field must match (AND).
// An entirely empty Match is the catch-all.
//
// The site and role keys from the Python original are deliberately absent:
// they were never populated there (no CMDB) and are not populated here either,
// so they could only ever fail to match. Naming conventions carry site
// information in most fleets, which is what NameRegex is for.
type Match struct {
	// Devices matches the device name exactly or as a shell glob ("fw*-dmz").
	// Case-insensitive.
	Devices []string `yaml:"devices"`

	// NameRegex matches the device name. Compiled and validated at load.
	NameRegex string `yaml:"name_regex"`

	// Platform matches the platform string, case-insensitively.
	//
	// Note the ordering trap: a platform rule needs a platform, but
	// fingerprinting requires connecting, which requires this decision. Feed
	// Device.Platform from the neighbor's CDP/LLDP claim, which the crawler
	// holds before it dials, rather than from a live fingerprint. An empty
	// Device.Platform never matches a platform rule.
	Platform []string `yaml:"platform"`

	// CIDRs matches the device address. Not in the Python original, which
	// matched on names because SC2 works from a device list; a crawl enqueues
	// bare addresses routinely, and a management range is often exactly the
	// thing a bastion rule wants to key on.
	CIDRs []string `yaml:"cidrs"`

	nameRe *regexp.Regexp
}

// IsCatchAll reports whether the match admits every device.
func (m Match) IsCatchAll() bool {
	return len(m.Devices) == 0 && m.NameRegex == "" &&
		len(m.Platform) == 0 && len(m.CIDRs) == 0
}

// Rule pairs a predicate with a jump target.
type Rule struct {
	Match Match  `yaml:"match"`
	Jump  string `yaml:"jump"`
}

// Device is what rules are matched against.
type Device struct {
	// Name is the device name, or the address if no name is known.
	Name string

	// Addr is the literal address, when known, for CIDR matching.
	Addr string

	// Platform is the vendor/platform string from the neighbor claim. Empty
	// before it is known; platform rules then do not match.
	Platform string

	// InheritedPath is the path used to reach the device that claimed this
	// one. Only consulted by TargetInherit.
	InheritedPath Path
}

// Hop is one bastion in a path, with the credential name to authenticate to
// it. No secret material.
type Hop struct {
	Name       string
	Host       string
	Port       int
	Credential string
}

// Path is an ordered list of hops, nearest first. An empty Path is a direct
// connection.
type Path []Hop

// IsDirect reports whether the path has no hops.
func (p Path) IsDirect() bool { return len(p) == 0 }

// String renders the path for logging, e.g. "lab-bastion -> dmz-jump".
// Credentials are named, never their material.
func (p Path) String() string {
	if len(p) == 0 {
		return TargetDirect
	}
	parts := make([]string, len(p))
	for i, h := range p {
		parts[i] = h.Name
	}
	return strings.Join(parts, " -> ")
}

// Decision is the outcome of resolving one device, including which rule
// produced it so a surprising path can be explained without re-deriving it.
type Decision struct {
	Path Path

	// RuleIndex is the position of the matching rule, or -1 when no rule
	// matched and the default (direct) applied.
	RuleIndex int

	// Target is the jump target the rule named: a host name, "direct", or
	// "inherit".
	Target string

	// Inherited is true when the path came from Device.InheritedPath.
	Inherited bool
}

// Resolver evaluates rules against devices. Safe for concurrent use after
// construction; it is read-only.
type Resolver struct {
	hosts map[string]Host
	rules []Rule
	log   func(format string, args ...any)
}

// Config is the parsed configuration. See config.go for loading.
type Config struct {
	Hosts map[string]Host `yaml:"jump_hosts"`
	Rules []Rule          `yaml:"proxy_rules"`
}

// NewResolver validates a config and returns a resolver.
//
// Validation is strict and fails loud, following the Python original's stance:
// a rule pointing at an undefined bastion, an uncompilable regex, or a cyclic
// via chain would otherwise surface as devices quietly connecting directly,
// which looks like a working crawl that silently skipped every bastion-only
// device.
func NewResolver(cfg Config, logf func(format string, args ...any)) (*Resolver, error) {
	hosts := make(map[string]Host, len(cfg.Hosts))
	for name, h := range cfg.Hosts {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, errors.New("jump: a jump host has an empty name")
		}
		if isReserved(name) {
			return nil, fmt.Errorf("jump: %q is a reserved jump target and cannot name a host", name)
		}
		if strings.TrimSpace(h.Host) == "" {
			return nil, fmt.Errorf("jump: jump host %q has no 'host' address", name)
		}
		if strings.TrimSpace(h.Credential) == "" {
			return nil, fmt.Errorf("jump: jump host %q has no 'credential'; name one explicitly", name)
		}
		h.Name = name
		if h.Port == 0 {
			h.Port = 22
		}
		hosts[name] = h
	}

	// via chains must resolve and must not cycle.
	for name := range hosts {
		if _, err := chain(hosts, name); err != nil {
			return nil, err
		}
	}

	rules := make([]Rule, len(cfg.Rules))
	copy(rules, cfg.Rules)
	for i := range rules {
		target := strings.TrimSpace(rules[i].Jump)
		if target == "" {
			return nil, fmt.Errorf("jump: rule %d has no 'jump' target", i)
		}
		rules[i].Jump = target
		if !isReserved(target) {
			if _, ok := hosts[target]; !ok {
				return nil, fmt.Errorf(
					"jump: rule %d points at undefined jump host %q", i, target)
			}
		}
		if rules[i].Match.NameRegex != "" {
			re, err := regexp.Compile(rules[i].Match.NameRegex)
			if err != nil {
				return nil, fmt.Errorf("jump: rule %d has an invalid name_regex: %w", i, err)
			}
			rules[i].Match.nameRe = re
		}
		for _, c := range rules[i].Match.CIDRs {
			if _, err := netip.ParsePrefix(strings.TrimSpace(c)); err != nil {
				return nil, fmt.Errorf("jump: rule %d has an invalid cidr %q: %w", i, c, err)
			}
		}
		if rules[i].Match.IsCatchAll() && i != len(rules)-1 {
			// Not fatal, but it makes every rule below it dead.
			if logf != nil {
				logf("jump: rule %d is a catch-all; rules %d and below are unreachable",
					i, i+1)
			}
		}
	}

	return &Resolver{hosts: hosts, rules: rules, log: logf}, nil
}

// Hosts returns the defined bastions, keyed by name.
func (r *Resolver) Hosts() map[string]Host {
	out := make(map[string]Host, len(r.hosts))
	for k, v := range r.hosts {
		out[k] = v
	}
	return out
}

// Resolve returns the path to reach d. Rules are evaluated top to bottom and
// the first match wins. With no matching rule, the connection is direct.
func (r *Resolver) Resolve(d Device) Decision {
	for i, rule := range r.rules {
		if !matches(rule.Match, d) {
			continue
		}
		dec := r.decide(rule.Jump, d)
		dec.RuleIndex = i
		return dec
	}
	return Decision{Path: nil, RuleIndex: -1, Target: TargetDirect}
}

func (r *Resolver) decide(target string, d Device) Decision {
	switch target {
	case TargetDirect:
		return Decision{Path: nil, Target: TargetDirect}

	case TargetInherit:
		if len(d.InheritedPath) == 0 {
			return Decision{Path: nil, Target: TargetInherit}
		}
		p := make(Path, len(d.InheritedPath))
		copy(p, d.InheritedPath)
		return Decision{Path: r.stripSelf(p, d), Target: TargetInherit, Inherited: true}

	default:
		p, err := chain(r.hosts, target)
		if err != nil {
			// Unreachable: NewResolver validated every chain. Fail to direct
			// rather than panicking mid-crawl.
			if r.log != nil {
				r.log("jump: %v", err)
			}
			return Decision{Path: nil, Target: target}
		}
		return Decision{Path: r.stripSelf(p, d), Target: target}
	}
}

// stripSelf removes any hop that is the device being dialed. A bastion reached
// through itself never connects, and the failure is opaque.
//
// The Python original handles this by convention - an explicit rule at the top
// mapping each bastion to direct. That rule still works here; this is the
// structural backstop for when someone forgets it.
func (r *Resolver) stripSelf(p Path, d Device) Path {
	out := p[:0:0]
	for _, h := range p {
		if isSelf(h, d) {
			if r.log != nil {
				r.log("jump: dropping hop %q from the path to %q (a bastion cannot be reached through itself)",
					h.Name, d.Name)
			}
			continue
		}
		out = append(out, h)
	}
	return out
}

func isSelf(h Hop, d Device) bool {
	if d.Name != "" && strings.EqualFold(h.Name, d.Name) {
		return true
	}
	if d.Name != "" && strings.EqualFold(h.Host, d.Name) {
		return true
	}
	if d.Addr != "" && strings.EqualFold(h.Host, d.Addr) {
		return true
	}
	return false
}

// chain walks the via links from name outward, nearest hop first, detecting
// cycles and undefined references.
func chain(hosts map[string]Host, name string) (Path, error) {
	var out Path
	seen := make(map[string]bool)
	for name != "" {
		if isReserved(name) {
			break
		}
		if seen[name] {
			return nil, fmt.Errorf("jump: via chain for %q is cyclic", name)
		}
		seen[name] = true

		h, ok := hosts[name]
		if !ok {
			return nil, fmt.Errorf("jump: undefined jump host %q referenced by via", name)
		}
		out = append(out, Hop{
			Name:       h.Name,
			Host:       h.Host,
			Port:       h.Port,
			Credential: h.Credential,
		})
		name = strings.TrimSpace(h.Via)
	}
	// Reverse: the outermost bastion is dialed first.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func isReserved(name string) bool {
	return name == TargetDirect || name == TargetInherit
}

// matches evaluates a predicate. Every populated field must match.
func matches(m Match, d Device) bool {
	if len(m.Devices) > 0 && !matchDevices(m.Devices, d.Name) {
		return false
	}
	if m.nameRe != nil && !m.nameRe.MatchString(d.Name) {
		return false
	}
	if len(m.Platform) > 0 {
		if d.Platform == "" {
			return false
		}
		found := false
		for _, p := range m.Platform {
			if strings.EqualFold(strings.TrimSpace(p), d.Platform) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(m.CIDRs) > 0 {
		if d.Addr == "" {
			return false
		}
		addr, err := netip.ParseAddr(d.Addr)
		if err != nil {
			return false
		}
		found := false
		for _, c := range m.CIDRs {
			pfx, err := netip.ParsePrefix(strings.TrimSpace(c))
			if err != nil {
				continue
			}
			if pfx.Contains(addr) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// matchDevices tests a name against exact and glob patterns, case-insensitively.
func matchDevices(patterns []string, name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if p == name {
			return true
		}
		if ok, err := path.Match(p, name); err == nil && ok {
			return true
		}
	}
	return false
}
