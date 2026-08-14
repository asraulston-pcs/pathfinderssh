# PathfinderSSH — Roadmap to AutoCon 6

**Target:** demoable, packaged, and buying-able before AutoCon 6 — Tucson, AZ,
16–20 November 2026 (workshops 16–17, conference 18–20).
**Runway from 28 July 2026:** 16 weeks.
**Author:** Scott Peterman
**Status:** plan of record

---

## The shape of v1

One binary. Terminal + crawl + map + config capture, sharing one engine:
**dial → fingerprint → run → { parse | store }.**

Nothing enters v1 unless it is that loop pointed at a new question.

Parsing lives in **discovery only**. The crawler parses because it has to turn
neighbor output into a graph. Capture does not parse — it stores what the
device said, verbatim. That split is the architecture, not a shortcut: it means
a new capture type is a row in a table, not code, and parsing can be layered on
top of a store that already exists rather than being a prerequisite for having
one.

**v1 platforms:** Cisco IOS / IOS-XE / NX-OS, Arista EOS, Juniper Junos.
**v1 OS target:** Windows, via the Microsoft Store. Linux/macOS builds exist
(they always have) but are not the store product.

### Explicitly out of v1

Parked, named here so they stop competing for the schedule:

- **Diff, of any kind.** Not text diff, not semantic diff. The store records
  what changed and when by virtue of only writing when content changes; reading
  the difference is a later feature and an easy one to add to a store that
  already holds every version.
- **Parsing in the capture path**, and everything downstream of it: structured
  records, record-identity keys, parse-trust scoring — the whole Netlapse
  engine. Capture stores raw text.
- Scripting / automation language, file transfer, NetBox & Nautobot
  integration, additional terminal emulations. All post-v1 directions.
- SNMP discovery, icon libraries, the viewer ecosystem. Never, per the vision.
- Any vendor family outside the three above.

---

## Hard dates that are not the ship date

| Date | Thing | Note |
|---|---|---|
| **30 July 2026** | Super Early Bird registration closes | Offered only to people paying their own way. Two days out. If the ticket is self-funded, this is a today decision. |
| **27 Aug – 29 Oct** | Regular registration | Standard tranche. |
| **Late September** | Full speaker list and program published | Which means the CFP is closing or closed **now**. Verify this week — a talk is worth more to the reputation goal than the product being one feature wider. |
| **~19 October** | Store submission deadline (self-imposed) | First submissions bounce. Four weeks of buffer before the conference — bought by rendering going external, and spent on this rather than on scope. |
| **16 November** | AutoCon 6 opens | |

---

## Phase 0 — Foundation (weeks 1–2 · 28 Jul – 9 Aug)

Cheap now, expensive later. The repo is two commits deep.

- [x] Collapse `POC/reachssh-go` and `POC/reachmaps` into one module.
      `reachmaps` was confirmed a strict superset before deletion — every `.go`
      file byte-identical, only `go.mod`/`go.sum` differed. `POC/reachssh-go`
      is gone; `POC/reachmaps` retains only the crawl output artifacts, which
      the `.gitignore` / lab-fixture items below still cover.
- [x] Set a real module path. `github.com/scottpeterman/pathfinderssh`.
- [x] Layout decision: **one module, everything under `internal/`**, with
      `cmd/crawl` and `cmd/reach` as the interim binaries until they collapse
      into `cmd/pathfinder`. The terminal is absorbed rather than kept as a
      separate module consuming this one — which is what `internal/` forecloses,
      deliberately: nothing outside this module can import any of it, so the
      closed side cannot leak into a public GPLv3 module by accident.
      Reversible with a `git mv` if the terminal ends up staying separate.
- [ ] `.gitignore`. Purge the committed binaries (~24 MB), the drawio temp
      file, and the crawl output artifacts from history.
- [ ] **Lab fixture map.** Crawl the lab bed, commit that `map.json` as the
      demo and test fixture. It replaces the production artifacts currently in
      the tree and becomes the screenshot source for every public asset from
      here on. Nothing with a real device name in it ships or gets posted.
- [ ] Single-source the version the way the terminal already does (one const,
      About dialog reads it).

**Exit:** one module, one binary that opens a terminal and can run a headless
crawl, clean history, lab fixture in place.

---

## Phase 1 — Credential vault (weeks 3–4 · 10–23 Aug)

This gates the map. Click-a-node-and-connect is only magic if it doesn't
prompt for a password on every node.

- [ ] OS-backed storage, no plaintext path: Windows Credential Manager
      (DPAPI), macOS Keychain, Linux Secret Service.
- [ ] Credential **sets**, not per-session secrets — a set binds to a domain
      suffix or address range, so both the crawler and click-to-session resolve
      without asking. The session schema already carries `credsid` UUIDs; this
      is hardening and widening what's behind them, not a new concept.
- [ ] Jump-host credentials as first-class members of a set. The crawler
      already needs them; the map click needs the same path.
- [ ] Break-glass: exportable, re-importable, and clearly not a sync service.

**Exit:** a crawl and a session both run from a named credential set with no
interactive prompt.

**Risk:** low. Known territory, three platform backends.

---

## Phase 2 — The map surface (weeks 5–6 · 24 Aug – 6 Sep)

**Rendering is external.** The app writes a self-contained HTML map and opens
it in the browser; a loopback listing API carries click-to-connect back into
the app. No Go layout algorithm, no rasterizer, no hit-testing, no fight with
Fyne's per-element draw. That decision removed this phase's risk, and with it
the schedule's single point of failure.

The viewer already exists in `secure_cartography_js`: `topology.js` is a
Cytoscape.js viewer that computes layout **client-side** across nine
algorithms, its Viewer tab already loads a standalone `map.json`, and its
`map.json` shape is the shape `topo.Generate` already emits. The integration is
writing the file the viewer already reads.

**Reuse boundary — take these and nothing else:** `topology.js`,
`drawio-export.js`, `assets/platform_map.json` and the icon set. Not `src/` —
that is the SNMP discovery engine, and reachmaps *is* the engine. `map.json` is
the contract between them. MIT, sole author, clean to fold into a closed
binary.

- [ ] Crawl from the GUI: seed, depth, domain, allow-domain, exclusions as a
      form; live progress with node counts; cancel that actually stops.
- [ ] **Self-contained HTML writer.** Vendor Cytoscape, Dagre, fCoSE, and Cola,
      `go:embed` them, and inline everything into one file. The Electron app
      loads these from CDN; a generated map must open on a jump box, an
      air-gapped segment, or conference wifi. It is also the right call
      independent of offline — a paid tool that calls out to a CDN to draw your
      network is a supply-chain surface and a bad look.
- [ ] **Richer platform string in `node_details`.** The icon chain matches
      longest-first on strings like `C9407R`; `NodeDetails.Platform` currently
      carries the fingerprint family slug (`juniper_junos`), so every node falls
      through to generic role detection and the map looks worse than SC2's does
      today. Mine the model from `netexec.Platform.VersionOutput` — already
      retained for exactly this — and add `vendor`. No new commands, so the
      read-only allowlist is untouched. The screenshot is the marketing; this is
      the difference between a map that demos and a map that sells.
- [ ] Click a node → terminal session opens with platform, paging, and jump
      path already known. This is the whole pitch. It works or the product
      doesn't.
- [ ] Leaves visible but visually distinct — mapped, never dialed.
- [ ] Redact option on the writer. The HTML is a shareable artifact by design,
      so it is a file people will email around with hostnames in it.

### The listing API — narrow by construction

A loopback HTTP listener inside a product holding credentials is the shape of
thing that earns a CVE. The constraints are requirements, not preferences:

- [ ] Bind `127.0.0.1` only, ephemeral port, listener lives and dies with the
      app.
- [ ] Per-run random token baked into the generated HTML, required on every
      request.
- [ ] Strict `Origin` and `Host` checks. DNS rebinding is the specific attack —
      a hostile page resolves a name to loopback and uses the browser as a
      confused deputy. Token plus origin check closes it.
- [ ] **The API takes node IDs, never hostnames and never commands.** The
      browser says "connect to node 7"; the app resolves 7 against its own crawl
      record. A compromised page cannot make the app dial an arbitrary host, and
      the attack surface is bounded by the map just built.
- [ ] Stale-token degradation: a map file outlives the process, so an old file
      opened after a restart shows a working map that simply cannot connect —
      never a mysterious failure.

**Risk:** low, and the risk that remains is the listener, not the rendering.

## Phase 3 — Capture and search (weeks 7–9 · 7–27 Sep)

Same engine, new question: "what did the device say." Config backup is the
first capture type shipped, not the only one the design admits.

### The capture model

A capture type is a **table row**, not code:

```
capture_type  platform     command                    timeout  max_bytes
config        arista_eos   show running-config        60s      8M
config        cisco_iosxe  show running-config        60s      8M
config        juniper_junos show configuration | display set  60s  8M
```

Ship with `config` populated for the three families. Everything else — arp,
mac, lldp, routes, version, inventory — is the same table with more rows, added
without a build. That is the whole point of not parsing: there is no template to
find, no key model to design, no per-type code path. The artifact is text.

- [ ] Capture-type table with per-type timeout and output-size ceiling. Both
      are mandatory fields — this is where "never hang a device" gets enforced
      for anything anyone adds later.
- [ ] Fetch, strip echo and prompt (already solved in `netexec`), store raw.
- [ ] **Fix `expect()` before capturing anything large.** It currently calls
      `Normalize(s.buf.String())` over the *entire* buffer on every read
      notification. At LLDP sizes that is invisible; at `show running-config`
      arriving in 32KB chunks it is quadratic — hundreds of iterations each
      regexing and rebuilding a multi-megabyte string. This is the one place the
      POC was tuned for small output, and it is what would make capture feel
      broken. Keep a scan offset, normalize and prompt-match only the tail,
      leave the raw bytes alone until the final drain.
- [ ] **Enforce the output ceiling in `readLoop`.** `s.buf` has no cap today;
      one oversized capture eats the process. `max_bytes` is where the table
      column becomes real.
- [ ] On-disk store: `<device>/<capture_type>/<timestamp>.txt`, plain files, no
      database. Content-hashed — an unchanged capture writes no new version, so
      the directory listing *is* the change history without anything computing
      a difference.
- [ ] Fan-out: select nodes on the map, or all of them, and capture. Build the
      selection model once; it is what "run one command across many devices"
      becomes later.

### Search — the actual requirement

- [ ] Full-text search across the whole store. Concurrent scan, no index. At
      realistic fleet size this is milliseconds in Go; an FTS index is a later
      optimization, not a v1 dependency.
- [ ] **Format-aware query normalization** — one token finds every
      representation. A MAC typed any way matches all four notations; an IP
      matches octet-anchored so `10.1.1.1` does not hit `110.1.1.10`; an ASN
      matches with and without the `AS` prefix. This is query rewriting, not
      parsing, and it is the difference between grep and something worth ten
      dollars.
- [ ] Results grouped by device and capture type, with the matching line in
      context and a click through to the full artifact.
- [ ] A hit on a device that is on the map opens a session from the result.
      That closes the loop: find it, then be on it.

**The scoping call, stated plainly:** this is *not* a Netlapse port and it is
not a step toward becoming one. Netlapse's value is parsing — structured
records, identity keys, semantic diffs, parse-trust scoring. That is a product,
and building it inside this window would consume the window. Pathfinder stores
text and finds text. If the two ever meet, it is Pathfinder exporting into
Netlapse.

**Risk:** low. Removing parsing removed the risk.

---

## Phase 4 — Harden and package (weeks 10–12 · 28 Sep – 18 Oct)

Security is a **requirement, not a phase**. The items below are the audit and
the proof; the work itself lands as each phase lands, because retrofitting a
security posture onto a finished product is how the posture ends up
aspirational. Specifically: the command allowlist gets built in Phase 3 with
the capture table, not here. Here it gets verified.

Nothing in this phase is negotiable against schedule.

- [ ] **Provably read-only** — the claim made real, and made to cover the
      capture table as well as the crawler. A test enumerates every command the
      binary can emit — crawler probes, fingerprint probes, and every row of the
      capture-type table — and asserts each against an allowlist. Adding a
      command that isn't on the list fails the build. This matters more now
      than it did last week: a table anyone can extend is exactly where a write
      command walks in wearing a `show` costume.
- [ ] Store security: capture artifacts are device configs. Permissions on the
      store directory, no world-readable default, and an explicit answer to
      "where does this live and who can read it" in the docs.
- [ ] **Host-key verification fails closed**, with no convenience override
      anywhere in the UI. Discovery's auto-accept-and-persist stays scoped to
      first contact on a dedicated known_hosts; mismatch always fails.
- [ ] **Never hang a device** — timeout audit on every path: dial, PTY, probe,
      per-command read, whole-crawl ceiling.
- [ ] Vault review against the Phase 1 exit criteria, cold.
- [ ] Leak scan over the repo, the docs, and the built binary's strings.
      Denylist populated with employer, domain, and hostname terms. This is the
      semantic pass, and it is the one that catches what heuristics miss.
- [ ] MSIX packaging, code signing certificate, store listing copy, screenshots
      from the lab fixture, privacy and support pages.
- [ ] **Submit by 19 October.**

---

## Phase 5 — Buffer and conference (weeks 13–16 · 19 Oct – 16 Nov)

- [ ] Demo rehearsed cold against the lab bed, on the laptop that goes to
      Tucson, with the conference wifi assumption of "there is none."
- [ ] Screenshot and short video assets. The distribution mechanism is the
      product itself: a map, and the caption about what drew it.
- [ ] Store bounce buffer. If the listing isn't live, a signed direct download
      demos identically.

---

## What this costs

Sixteen weeks, solo, alongside sole-network-engineer on-call and an active job
search. That is the real constraint, and it means this roadmap is only
achievable if the rest of the portfolio genuinely stops for the duration:
Netlapse, waypostdns, uglyfruit, SC2 Python, and new open-source feature work
on the terminal all park until 20 November. Maintenance-only.

Moving rendering out of the app bought roughly two weeks. Those weeks are
already spent — on an earlier store submission and a four-week buffer, not on
scope. Nothing new enters because the schedule loosened.

### Where the risk sits now

The rendering risk is gone. What is left, in order:

1. **The calendar.** A job change landing mid-window is still the thing most
   likely to end this.
2. **Store packaging and signing.** Unknown-unknowns, and the only item whose
   duration is not under your control. It is why submission is 19 October.
3. **The loopback listener.** Small surface, high consequence. It is the one
   piece of new attack surface the product gains, and it gets built to the
   Phase 2 constraints or it does not ship.
   

### If something has to give

1. Format-aware query normalization — plain substring search still works
2. Capture types beyond `config` — the table stays, the rows wait
3. Extra layout algorithms and viewer polish — one good default layout demos
4. Capture and search entirely — the map alone is the demo

Never the security requirements. They are not in the cut order at any position.