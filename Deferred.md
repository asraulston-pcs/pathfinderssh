# Deferred — by module

Known-and-accepted debt. This file is the **index and the schedule**: what is
deferred, and what un-defers it. The architecture docs stay authoritative for
*why* — each entry points at the section that explains it, and those sections
are written to be read next to the code they describe.

**Every entry carries a trigger.** A trigger is the event that makes the item
stop being deferred, not a priority. "Later" is not a trigger. If an item has
no plausible trigger, it is not deferred — it is declined, and it says so.

**What can never appear here:** the four security non-negotiables — credential
storage with no plaintext path, host-key verification that fails closed, a
provably read-only crawler, and never hanging a device. Those finish on their
own terms and live in `ROADMAP.md`. A security item on a deferred list is a
security item that ships deferred.

---

## internal/crawler · internal/credres · internal/jump

One root cause with three consumers. Described in full in
`README_Arch_Maps.md` §Known gaps and `README_Arch_Creds.md` §What is not built.

| Item | Trigger |
|---|---|
| **BFS queue carries no claim.** The queue item is `{target, reported, identity, depth}` — not which device claimed this one, or what the claim said. `DialTarget` therefore has no `Platform`, so platform-scoped credentials and platform-matched jump rules are inert on the crawl path; and `jump`'s `inherit` target has nothing to inherit from. | Click-to-connect. A session opened from the map needs the jump path that reached the device, and the path cannot survive to the click if it does not survive enqueue. **Write a queue-parentage test before the change** — a path that silently fails to propagate looks exactly like a device that never needed one. |
| **`DialTarget.Addr` is thin.** Set only when the dial target is itself an address, though the neighbor claim carries a `RemoteIP` for named devices too. | Same change, same root cause. |
| **`dialAllowed` runs on the reported name, before resolution.** A CGNAT neighbor is never dialed under an allowlist even when its PTR sits under an allowed domain. | **Declined, not deferred.** Deliberately unchanged: this is a policy change, not a correctness fix. Revisit only if a real crawl loses devices to it. |

## internal/sshcore

`README_SSH_Arch.md` §What is not built.

| Item | Trigger |
|---|---|
| **Multi-hop jump dialing does not exist.** Single-hop `-jump` only. `jump` can resolve a chained path; nothing can dial one. | First consumption of `jump.BoundPath`. This is the one piece of genuinely new connection code left. |
| **Windows SSH agent unsupported.** `agentAuth()` returns nil on Windows; named-pipe agent support deferred. Keys and passwords work. | The Windows store build. Windows is the v1 target platform, so this deferral sits on the shipping OS — verify it is acceptable rather than assuming. |
| **No connection pooling.** `OwnsClient` makes a shared client possible; nothing shares one. Crawl opens and closes per device; the terminal dials its own. | Reusing a crawl's client for a click-to-connect session. Optional even then — a fresh dial is correct, just slower. |
| **No package tests.** | **Declined.** The honest test is a container sshd, which is not a `go test` line. Covered indirectly by `term` and `netexec`. |

## internal/netexec

Both items below were previously tracked as ROADMAP Phase 3 checkboxes. They
are module debt with a trigger, not roadmap scope.

| Item | Trigger |
|---|---|
| **`expect()` re-normalizes the entire buffer on every read notification.** Invisible at LLDP sizes; quadratic on a `show running-config` arriving in 32KB chunks — hundreds of iterations each regexing and rebuilding a multi-megabyte string. Fix: keep a scan offset, normalize and prompt-match the tail only, leave raw bytes alone until the final drain. | First config capture. This is the one place the POC was tuned for small output, and it is what would make capture feel broken. |
| **`s.buf` has no ceiling.** One oversized capture eats the process. | First config capture — this is where the capture table's `max_bytes` column becomes real. |
| Coverage 10.2%, lowest non-trivial package in the tree. | The capture path landing on top of it. |

## internal/tfsm

| Item | Trigger |
|---|---|
| **No tests.** Template selection is exact platform+command-key lookup; a miss is a config error, so there is little logic to test — but the templates themselves are unexercised. | Next template addition, or first platform added beyond the v1 three. |

## internal/topo

| Item | Trigger |
|---|---|
| **`NodeDetails.Platform` carries the fingerprint family slug** (`juniper_junos`), not a model string. The icon chain matches longest-first on strings like `C9407R`, so every node falls through to generic role detection. The model is available in `netexec.Platform.VersionOutput`, already retained for this. Add `vendor` alongside. | The HTML map writer. Do it *first* in that phase: no new commands, so the read-only surface is untouched, and it is the difference between a map that demos and a map that sells. |

## internal/vault

| Item | Trigger |
|---|---|
| **Credential rotation / expiry.** `LastUsed` is written and nothing reads it. | **Declined for v1.** Not in the vision, not in the roadmap. The field costs nothing to keep. |
| Coverage 43.9% — lowest of the security-critical set (`normalize` 94, `credres` 60, `topo` 80). | Phase 4 cold vault review. Crypto paths have had a cold read; the record-management paths have not. |

## internal/capture

See `README_Capture_Arch.md` for why the store is laid out the way it is.

| Item | Trigger |
|---|---|
| **No per-type summary alongside `history.jsonl`.** The store tab's device list shows name, platform and last-captured from `device.json` alone, because a list-level column like "3 of 4 types captured last night" would cost devices x types x history-depth on the screen that opens first. `history.jsonl` records every attempt including unchanged ones — a nightly schedule with four types writes ~1,460 lines per type per year, and `readHistory` parses all of them. Fix is a small summary written beside the history on each `Put`, so the list reads one line instead of thousands. | Either of: the first store that is slow to open, or the first request for a per-device health column. Not before — the summary is a second copy of a derived fact, and a derived copy that nothing needs yet is a consistency bug waiting for a reason to exist. |
| **`Types()` and `History()` read whole files.** Same root cause as above, scoped to one device, which is why it is acceptable: bounded by that device's own history rather than by the estate. | Same trigger. A device with years of daily captures is the first place it shows. |

## internal/ui

No architecture doc exists for this layer — see repo-wide below.

| Item | Trigger |
|---|---|
| **Remaining unwired exported API.** The port left a public surface rather than a curated one. What survives after the cull and the theme reduction is the genuinely-unwired set: `ShowFind`, `SetAntiIdle`, `ResolveAntiIdle`, `AntiIdleKeystrokeChoices`, `GetTitle`, `GetContext`, `IsOpen`, the `HybridScrollContainer` scroll methods. | After the shell exists and it is clear what it calls — not before, because "unreferenced" and "not wired up yet" are indistinguishable until a real consumer lands. |
| **OSC window-title handling is not wired.** `extractWindowTitle` parses the sequence and nothing consumes the result; it carries a `NOT WIRED` doc comment so a dead-code scan does not take it again. | A tabbed UI that has somewhere to put a title. |
| **Selection under the alternate screen is unfinished.** `updateAltScreenSelectionOverlay` is on pfterm's manual checklist and has never been signed off. Also marked `NOT WIRED`. | Working the alternate-screen item on the pfterm checklist. |

## cmd/pfterm

| Item | Trigger |
|---|---|

## Repo-wide

| Item | Trigger |
|---|---|
| **No terminal/UI architecture doc.** `README_Arch_Creds.md`, `README_Arch_Maps.md` and `README_SSH_Arch.md` cover three layers; the terminal has none, and `README_SSH_Arch.md` already forward-references it ("see the terminal layer"). | Before Phase 2. The map surface plugs directly into this layer, and it is the layer with no written contract. |
| **No single-source version const.** `cmd/pathfinder` has `var version`, stamped by `-ldflags`; the other four front ends have nothing, so `pfterm --version` and its siblings cannot answer. | Phase 0 exit / first packaged build. |
| **Trailing newlines missing on 7 files.** | Any `./test.sh` run — the gofmt stage rewrites them. Noted only so it is not mistaken for drift. |