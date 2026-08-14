# Mapping Architecture

How PathfinderSSH turns one seed device into a topology map.

Nine packages and three commands:

    internal/sshcore    dial, algorithms, auth, host-key policy
    internal/netexec    PTY shell session, paging, fingerprint, output cleanup
    internal/tfsm       embedded templates, exact platform+command selection
    internal/normalize  interface, identity, prompt, and platform canonicalization
    internal/crawler    depth-batched BFS, per-platform neighbor plans
    internal/topo       map.json generation, bidirectional link validation
    internal/mapweb     the viewer: loopback server, embedded page, node table
    internal/crawldial  parameters -> a configured crawler, for both front ends
    internal/crawlrun   a crawl as state: events, rows, counts, run-to-run diff
    cmd/crawl           the CLI
    cmd/crawlui         the window
    cmd/mapview         the viewer harness: serve one map, open it, log clicks

`internal/ui/mapdialog.go` is the picker the shell opens; `cmd/pathfinder`
owns the server and the click-to-session callback.

`internal/crawlrun` is described in README_Arch_Runs.md; it is the answer to
"what happened", where everything else here is the answer to "what is the
network". It has no dependency on the crawler or on any toolkit, deliberately.

---

## The problem this solves

Point it at one device. Get the network back.

Everything else follows from that sentence being taken literally. No SNMP
community to arrange, no agent to install, no inventory to import, no API to
integrate. SSH is the one interface that exists on every device in a mixed
fabric and that an engineer already has access to, so SSH is the only transport.

The consequence is that the crawler does everything through a shell session
against a device that was designed for a human. It reads command output meant to
be looked at. This is not elegant and it is the entire reason the tool works
anywhere.

Two rules constrain the whole design:

**Read-only, provably.** Every probe is exec-level and side-effect-free. This is
a hard codebase rule, not a convention — the tool runs against production
infrastructure that nobody authorized it to change, and "it only reads" has to
be true by inspection of the command list, not by trusting a mode flag.

**Never hang a device.** Every read is bounded by a timeout. A crawl that wedges
a session on a router has caused an outage, and no map is worth that.

---

## The pipeline

Per device:

    dial ──▶ open shell ──▶ fingerprint ──▶ select plan ──▶ run commands
                                                                  │
     map.json ◀── generate ◀── claim + enqueue ◀── normalize ◀── parse

And once, at the end:

    map.json ──▶ SetMap ──▶ browser ──▶ click ──▶ session dialog ──▶ terminal

Across devices: breadth-first, batched by depth, concurrent within a batch.

    seeds ──▶ depth 0 ──▶ neighbors ──▶ depth 1 ──▶ neighbors ──▶ depth 2 ...
                 │                          │
                 └── claim set ─────────────┘   (one device, crawled once)

---

## internal/sshcore — the connection

Dial, algorithm policy, authentication, host-key verification. Shared with the
`reach` single-host CLI and, eventually, with the terminal.

Two things worth naming.

**Algorithm policy is tiered.** A modern default set, with a legacy tail (old
KEX groups, CBC ciphers, sha1 MACs, ssh-rsa/ssh-dss) appended only when
`LegacyAlgorithms` is set. Network gear outlives its crypto by a decade, and a
tool that cannot reach the old console server is a tool that cannot map the
network — but reaching it must not weaken every other connection in the run.

**Host-key verification fails closed and is not overridable by convenience.** A
key *mismatch* is always an error. A key that is merely *unknown* is subject to
policy: strict rejects it, TOFU accepts on first contact via a callback and
persists it. The crawler supplies a callback that auto-accepts and logs, which
is the right trade for discovery — but the mismatch path is not reachable from
there, deliberately.

`Client.Addr()` returns the string that was dialed. `Client.RemoteAddr()`
returns the connection's actual peer address. The distinction matters: recording
the dialed string as a device's address meant a name-dialed device stored its
*name* where its address belonged, and the claim set never learned the address
— so the same device, reported by a neighbor as an IP, was crawled and mapped a
second time.

---

## internal/netexec — the shell

An interactive PTY shell driven by prompt matching. This is the behavior layer
ported from `reachssh`, and it is where most of the vendor reality lives.

**Why a PTY shell and not `exec`.** Plenty of network devices do not implement
`exec` channels usefully, or implement them with different paging behavior than
the interactive shell. The shell works everywhere.

**Prompt matching is anchored to the last line.** A mid-output line that happens
to look prompt-ish would otherwise end a read early and truncate the output —
which produces a parse that succeeds and yields half a device's neighbors, the
worst possible failure shape.

**Paging is disabled once, after the first prompt.** `terminal length 0`,
`set cli screen-length 0`, and so on. Without it every long output stalls on a
`--More--` that the parser never sees.

**Output is cleaned before parsing.** `StripEchoAndPrompt` removes the echoed
command from the head and the prompt line from the tail. Conservative on the
echo side, because devices variously echo the bare command, the prompt plus the
command, or a line-wrapped version of either.

`Fingerprint` runs a version command and matches the output against a table to
produce a platform string — `arista_eos`, `cisco_nxos`, `cisco_iosxe`,
`cisco_ios`, `juniper_junos`, and a few others. Everything downstream keys off
that string.

`Session.Prompt()` retains the last prompt line. On a device reached by address
that is the only place the device's own name appears.

---

## internal/tfsm — parsing

Embedded TextFSM templates with **exact selection**: the platform string from
the fingerprint plus a logical command key maps to exactly one template.

```
arista_eos  + lldp_detail  ->  arista_eos_show_lldp_neighbors_detail2.textfsm
cisco_ios   + lldp_detail  ->  cisco_ios_show_lldp_neighbors_detail.textfsm
```

No scoring, no fallback chain. The template folder is frozen and validated
against captured device output, so a lookup miss is a configuration error rather
than a case to degrade through. (This is deliberately the opposite choice from
the scoring engine in `tfsm-fire`, which solves a different problem: there, the
output is untrusted and the template has to be *discovered*. Here the platform
is already known, and guessing would only hide a missing template.)

Command keys are logical — `lldp`, `lldp_detail`, `cdp`, `cdp_detail` —
decoupled from the exact CLI string, which lives in the crawler's plan. The same
template serves IOS and IOS-XE because they share an output family.

**This folder is the moat.** Not the code. The variant choices, the EOF-record
behavior, the field quirks — those exist because two real devices described the
same link differently and someone had to sit with the output until it parsed.
That knowledge does not regenerate from a spec.

Which makes its current test coverage the most conspicuous gap in the project.
The validation lives in an external harness; in-tree, a one-character edit to a
template compiles, ships, and silently parses zero records.

---

## internal/normalize — canonicalization

The unglamorous package that decides when two strings mean the same thing.

**Interfaces.** `Eth1/1`, `Ethernet1/1`, `et-0/0/48`, `xe-0/0/12` — the same
link is described differently by each end, and an edge is deduplicated on
`(local_if, peer, remote_if)`. Without normalization every link appears twice.

**Identity.** Lowercase, trailing dot, domain suffix stripping, and the CGNAT
rule with a forward-confirmed PTR. One implementation, shared with the
credential resolver — see the credentials document for why that consolidation
mattered.

**Artifact and MAC names.** Some devices report a neighbor by chassis MAC, or by
a placeholder string that is not a name at all. Both are detected: a MAC-named
neighbor falls back to its reported management IP, and an artifact name is
skipped entirely.

**Prompt to hostname.** `HostnameFromPrompt` strips vendor decoration — trailing
`#>$%`, nested `(config-if-Et1)` modes, the Junos `user@` prefix, the
`{master:0}` and `[edit]` banner lines above the prompt. It refuses anything
that does not come out hostname-shaped, and that refusal is the important part:
a wrong name silently merges two devices into one map node, which is worse than
a node labelled with an IP.

**Platform from description.** CDP carries a platform field; LLDP does not, and
carries the platform as prose inside the system description. Every LLDP template
here captures a description and none capture a platform — so before this
existed, every neighbor discovered over LLDP reported no platform at all, which
on an Arista/Junos fabric is every neighbor. It maps the description onto the
same tokens the fingerprinter produces, so a claimed platform and a
fingerprinted one are comparable. An unrecognized vendor returns empty rather
than a guess.

---

## internal/crawler — the walk

Breadth-first, batched by depth, with a worker pool per depth level.

### The dial seam

The crawler decides *which device it means*. It does not know how a connection
gets made.

```go
type DialTarget struct {
    Target   string // what to dial, post-resolution — a transport detail
    Reported string // the claim as received, pre-resolution — diagnostics
    Identity string // the crawler's claim key — what every cache keys on
    Addr     string // literal address when known, for CIDR scope matching
    Depth    int
}

type DialFunc func(t DialTarget) (*sshcore.Client, error)
```

Everything about credentials, bastions, algorithms, and host-key policy lives on
the other side of that function. The crawler is testable without SSH because of
it, and the credential resolver was wired in without the crawler learning that
credentials exist.

`Identity` is the load-bearing field. Anything caching per device keys on it and
never on `Target`, because a box dialed by address on one hop and by name on the
next is one device — and keying on the dial string warms two cache entries that
each help half the crawl.

### Resolve, then claim, then dial

Order matters and used to be wrong.

```go
func (c *Crawler) admit(r resolution, reported string, depth int) (item, bool)
```

`resolve` applies the CGNAT rule and domain completion. `admit` derives the
identity from the *resolved* string and claims it. Only then is the device
enqueued.

When resolution happened after the claim, a CGNAT address whose PTR resolved was
claimed under the address and dialed by the name — two keys for one device the
moment anything downstream started caching on identity.

### The claim set

One device gets crawled once. `tryClaim` registers both the canonical identity
and the short name, and `registerAliases` adds the hostname, sysname, and
address of a device *after* it has been crawled, so the same box reached by a
different name later is not re-crawled.

### What gets dialed, and what does not

A neighbor claim is evidence a device exists. Dialing it is a separate decision
with several gates:

| Gate | Behavior |
|------|----------|
| Artifact / MAC name | Falls back to reported IP, or is skipped |
| `-exclude` pattern | Matched against the *claim* — mapped as a leaf, never dialed |
| `-allow-domain` | Only names under an allowed suffix are dialed; others map as leaves |
| Resolvability | A name with no record after domain completion maps as a leaf |
| `-depth` | Enqueue stops at the configured depth |

Exclusion by claim matters at scale: the LLDP description already says the
neighbor is a server or an out-of-band controller, so there is no reason to
spend a connection finding that out.

`-allow-domain` is essential anywhere a seed faces an internet exchange or a
shared fabric, where LLDP reports a peering partner's routers. They belong in
the map — they are really connected — and they must never be dialed.

### Per-platform plans

Which commands to run, and which template parses each. The plan encodes field
findings that only exist because someone hit them:

- **EOS**: `lldp detail` alone carries everything.
- **IOS / IOS-XE**: some builds omit `Local Intf` from `lldp detail` entirely,
  so edges come from the plain `lldp` table and detail is enrichment. `cdp
  detail` does carry a local interface and a management IP for crawl targets.
- **NX-OS**: `cdp detail` plus `lldp detail`.
- **Junos**: newer builds accept `show lldp neighbors detail`; older ones reject
  it. The plan starts with the terse table, which is always valid, and treats
  detail as best-effort.

"Best-effort" is a first-class property of a plan step. A step that fails or
parses to nothing logs and continues; a required step failing fails the device.
Without that distinction a single old Junos build turns into a hole in the map.

---

## internal/topo — the map

`Generate` produces `map.json`, structurally compatible with the Python
discovery engine's output so existing viewers and seed-artifact consumers work
unchanged. It is also what the viewer below reads — `internal/mapweb` parses
this shape and nothing else.

```json
{
  "lab-r1.site1.lab.example": {
    "node_details": { "ip": "10.20.0.11", "platform": "arista_eos" },
    "peers": {
      "lab-qfx1.site1.lab.example": {
        "ip": "10.20.0.21",
        "platform": "juniper_junos",
        "connections": [["Eth49/1", "et-0/0/48"]]
      }
    }
  }
}
```

**Bidirectional link validation is implemented here**, which is the one
deliberate departure from the Python original — where the equivalent check is
dead code that trusts every one-sided claim.

The rule: a link between two *discovered* devices must be claimed by both sides.
A claim toward an undiscovered peer — a server, a partner's router, anything
that was never dialed — is trusted, because it is the only evidence that will
ever exist for that link.

The asymmetry is the point. If both ends were crawled and only one reports the
link, something is wrong — a stale LLDP entry, a half-configured port, a
neighbor table that has not aged out — and drawing it as a real link launders a
fault into a fact. `-trust-unidirectional` restores Python parity for anyone who
wants it.

`StripDomains` removes configured suffixes from every identity before matching,
so a device seen short and fully qualified merges into one node. Site labels
beneath the suffix survive.

---

---

## internal/mapweb — the viewer

`map.json` is a file. Turning it into something a person looks at is a separate
problem with a separate answer: a browser.

**Why the renderer is not a widget.** A browser already has a graph engine, a
zoom, a scroll, a text search and a print dialog, none of which has to be built
or maintained here. The toolkit has no embedded browser and the ones that exist
are cgo against a platform web view — a dependency that buys a window frame and
costs a packaging story. So the map renders out of process, and what stays in
the application is the one thing only the application can do: know which device
a node is, and open a session to it.

    cmd/pathfinder ──▶ mapweb.Serve ──▶ 127.0.0.1:<random>
                         │                    │
                     SetMap(bytes)         browser
                         │                    │
                     OnConnect ◀── POST /api/connect {id}

One server per app session, started on the first map opened. `SetMap` replaces
the loaded map without restarting anything, so the port and the token survive
and a browser tab left open shows the next map on a reload.

### The threat is the browser, not the network

The listener is on the loopback address and never leaves the machine. That is
not the interesting part. The interesting part is that *every other page the
user has open* can also reach `127.0.0.1`, and this particular local service
will open an SSH session on request.

Four checks, none of which costs the real page anything:

| Check | Stops |
|-------|-------|
| Per-run token, sent as a header | Any page that did not receive our URL |
| `Origin` must be ours | A page that somehow has the token posting from elsewhere |
| `Host` must be ours | DNS rebinding — the one header an attacker cannot forge |
| Opaque node IDs | Naming a host that is not in the loaded map |

The IDs are the structural one. The request body a page can send never contains
a hostname or an address, only an opaque per-map identifier, so the set of hosts
reachable through this surface is exactly the set in the map currently loaded.
`SetMap` re-rolls every ID, which means a tab left open on the previous map
cannot connect into the new one.

The page itself is deliberately **not** gated: it carries no data, and
everything it displays arrives through the API. A stranger who opens the port
gets an empty viewer.

And a click never dials. It opens the session *dialog*, prefilled. A request
that arrives over HTTP should end in a form somebody confirms, which is also
what makes leaving the surface running safe.

### The page

Assets are embedded, so the binary is the viewer — there is no asset directory
to ship, no CDN to reach, and no version skew between the map and the engine
that draws it.

    assets/index.html            layout and theme
    assets/app.js                controller: fetch, filters, detail, connect
    assets/viewer.js             the Cytoscape viewer
    assets/platform_map.json     platform string -> icon
    assets/vendor/cytoscape.min.js   the one third-party dependency (MIT)

`cytoscape.min.js` is named in a *second* `go:embed` directive beside the
directory pattern. The directory alone embeds whatever happens to be there and
says nothing when the graph engine is missing — the build succeeds and the page
404s at runtime, where a missing script presents as a MIME error rather than a
missing file. Naming it makes its absence a compile error, and costs nothing:
the embed FS deduplicates by path.

Layouts are cytoscape core only — breadth-first, force-directed, concentric,
circle, grid. `dagre`, `fcose` and `cola` are extensions this build does not
ship; the layout table still names them and falls back to a core layout when
they are absent, so turning one on is a script tag.

Icons resolve in three tiers: an exact platform-substring match, then looser
rules that also look at the hostname, then role detection from the platform
string alone. The platform map is optional — without it the third tier still
picks something reasonable. What the map actually buys is the cases where the
platform string is not about network gear at all: a Linux host draws as a
server, an access point as an access point, rather than everything landing on a
switch.

Two things the viewer does *not* do, because the crawler already did them:
strip domain suffixes (that is `topo.StripDomains`, applied at generation) and
decide what a node is called.

### Listing and picking

`ListMaps` reads a folder and describes every `.json` in it — but it parses each
one rather than stat-ing it, so a row reads `13 devices, 1 leaf · 2h ago`
instead of `41 KB`. Parsing doubles as the openable check.

A file that fails to parse is **listed with the reason, not hidden**. A map from
a run that died halfway is precisely the file somebody goes looking for, and a
picker that silently omits it sends them to check whether the crawl wrote
anything at all.

Newest first, because after a crawl that is the one wanted.

The picker itself is list-and-open. There is no rename, delete or archive:
these are files in a folder the operator owns, and a file manager is better at
managing them than a dialog inside a terminal application will ever be.

### cmd/mapview

A harness, not a product surface — the same relationship `cmd/crawlui` has to
the shell. It serves one map, opens it, and logs a click instead of opening a
session, which is how the viewer gets debugged with no vault, no crawl and no
window in the way.

`-connect` is **off by default** there. The harness cannot open a session, so
advertising a Connect button would be the application claiming a capability it
does not have — and a button that dials nothing reads as a broken device rather
than a harness.

---

## Two front ends, one crawler

`cmd/crawl` and `cmd/crawlui` do not assemble their own crawlers. Both go
through `crawldial.Build`, which turns a `crawlrun.Params` into a configured
`crawler.Crawler`, and through `crawldial.MapOptions`, which turns the same
parameters into `topo.Options`.

This is not tidiness. Two front ends building their own would drift — a default
changed in one, a binding store opened at a different path in the other — and
the symptom is a crawl that behaves one way from the terminal and another way
from the window, which is a miserable thing to chase. It has already happened
once: `cmd/crawlui` was wired with `StripDomains` and quietly without
`TrustUnidirectional`, so the window produced a map with fewer links and
nothing in the output said which links were missing. A dropped map option is
invisible in its own result.

`MapOptions` is guarded by a reflection test that fails if a field is ever
added to `topo.Options` without being derived from `Params`.

## Observation is additive

The crawler carries two observation surfaces:

    Config.Log   printf progress lines; nil discards
    Config.Emit  structured events; nil costs nothing

Every emit sits beside the `cfg.Log` call it mirrors, and both fire from the
same site. That duplication is deliberate and load-bearing: it means adding or
changing a UI can never change what the CLI prints, and the log stays free to
be reworded because nothing parses it. A UI that scrapes log text turns every
message into an API.

The one emit with no log line beside it is `KindReached` — the crawler only
narrates what goes wrong, so success has nothing to pair with. That asymmetry
caused a bug worth remembering: with no success event, every device that
worked stayed mid-flight and was swept into a failure at the end of the run.

Events key on the **claim identity**, never on `Hostname`. With a `-domain`
suffix configured the two differ, and keying terminal events on the dialed
string files them against a second row while the first sits unfinished.

## Known gaps

**Multi-hop dialing does not exist.** `sshcore` does single-hop `-jump`. The
jump package can resolve a chained path; nothing can dial one.

**`sshcore.Dial` takes no context.** The crawl is cancellable — `CrawlContext`
checks between depth batches and again inside each worker once it holds a
semaphore slot — but a connection already inside a TCP connect or a key
exchange runs to its own timeout. Worst-case stop latency is one device's dial
timeout, not the whole batch. The fix is `net.Dialer.DialContext` inside
`sshcore`.

**`DialTarget` has no `Platform`.** The neighbor's `RemotePlatform` is exactly
the pre-dial hint that platform-scoped credentials and platform-matched jump
rules want, and it is still dropped at enqueue. The queue now carries
parentage (`item.parent`, below), so this is no longer blocked on the same
root cause — it is simply not done.

**`Addr` is thin.** Set only when the dial target is itself an address, though
the neighbor claim carries a `RemoteIP` for named devices too.

**Parentage exists but nothing routes on it.** `item.parent` carries the
identity of the device whose neighbor table produced this one, set by `admit`
and surfaced as the `Via` column. `jump`'s `inherit` target — reuse the path
that reached the claiming neighbor — now has something to inherit from, and
still does not use it.

**`sshcore` has no tests.** That is defensible as far as it goes —
the honest test is a container sshd, which is not a `go test` line — but
`internal/fakedev` is now an in-process SSH server, so the excuse has a trigger
against it. `tfsm` does have tests now; what it still lacks is validation of the
*templates*, which is the gap named above.

**`dialAllowed` runs on the reported name, before resolution.** So a CGNAT
neighbor is never dialed under an allowlist even when its PTR sits under an
allowed domain. Arguably wrong, deliberately unchanged — it is a policy change
rather than a correctness fix.

### The viewer's

**The leaf filter hides on degree, not on discovery.** "Hide leaf nodes" removes
anything with one link, which on a real map also removes crawled devices that
happen to sit at the end of a chain. On the lab map the two sets coincide; on a
large one they do not.

**Raising the window is best-effort.** After a click the browser has focus, so
the shell calls `RequestFocus` to bring the session dialog forward. Fyne makes
that a no-op under Wayland by design, where the compositor decides — so on a
Wayland session the tab is correct and the window does not come forward.

**One node per click.** Selecting several nodes and opening them together is the
obvious next thing a map is good for, and would turn N window switches into one.

**No export beyond PNG and the raw JSON.** The draw.io exporter from the
JavaScript lineage is not carried over.

**Nothing opens the map a crawl just wrote.** The crawl finishes, the map is
written, and the person goes to the picker and selects the file at the top of
the list. One line on the completion handler would close that.

**The HUD integration is not here.** Launching a per-device real-time view from
a node is the interesting version of this surface and is deliberately out of
v1 — with the constraint recorded now, while it is cheap: if actions ever
become extensible, the action list must be registered by the application at
startup, with the page able to name only a node and an action. A configuration
file that says which command to run hands the choice of command to whatever can
reach the port.