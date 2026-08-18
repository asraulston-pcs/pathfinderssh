# Capture Architecture

How PathfinderSSH reads device state and keeps it.

    internal/capture      the engine: connect, identify, read, store
    internal/capturerun   events, the run model, parameters
    internal/capturedial  one Build both front ends go through
    cmd/capture           the harness

`internal/capture` imports no toolkit. `internal/capturerun` imports neither a
toolkit nor the engine. Those two constraints are what make the whole subsystem
testable without a display and without a device, and they are the reason the
run model could be written alongside the engine rather than fitted to it
afterwards — which is what happened to `crawlrun`, and what cost a session of
chasing a missing emit site.

---

## The problem this solves

A config backup is only trustworthy if you can tell what it did *not* do.

Every backup tool reports what it collected. The useful question is the other
one: of the devices you asked about, which came back unchanged, which had
nothing to give, and which failed — and can you tell those three apart at a
glance six weeks later, when the device you need is the one that quietly
stopped answering in week two.

That is why the unit here is a **(device, capture type) pair** rather than a
device. A box whose running config came back fine and whose MAC table timed out
is not a failed device. Reducing it to one status picks a lie either way: call
it failed and someone investigates a device that is working, call it fine and
the missing artifact never surfaces.

---

## Specs

A capture type is a `Spec`: a name, a description, a retention bound, and a
per-platform command.

    Spec{Type, Description, Keep, Commands map[platform]Command}
    Command{Command, MaxBytes, Timeout, Cost, ModelMatch}

Built-ins live in Go source, not a data file. That is deliberate and it is
about the read-only guarantee below: a build-failing allowlist test cannot
cover a file the user edits at runtime. Extending the set means adding a `var`
— no registry, no `init()` side effects, no plugin loader.

A spec with no command for a platform is **not applicable**. That is a third
outcome, distinct from failure, and it exists for the same reason crawl's
"not dialed" does. Junos has no `startup-config`; without the third outcome
every Junos box in the estate reads as a permanent failure and the failure
column stops meaning anything.

### What ships

| Type | Keep | Why |
| --- | --- | --- |
| `running-config` | unlimited | The record. It only changes when somebody changes it. |
| `startup-config` | unlimited | Separate from running on purpose: the interesting question is whether they differ, and that cannot be asked if they share a file. |
| `inventory` | unlimited | Proof the model is not config-shaped — same storage, same history, different kind of state. |
| `arp-table` | 5 | Bounded. Changes because time passed. |
| `mac-table` | 5 | Bounded, and the one type that needs a per-model gate. |

The first three are the original set. The last two arrived later and are the
reason two things exist that did not before: **retention**, because a type that
differs on every read has no natural stopping point, and **applicability
gating**, because a MAC table is a question a router cannot answer.

`show tech-support` was a builtin and was deliberately removed. It became one
tick against a whole estate the moment the type picker became a check group,
which is a diagnostic collection nobody asked for. Its two allowlist entries
went with it, because the allowlist test fails on a permission granted to
nobody.

### Retention

`Keep` bounds how many distinct versions of a type the store holds **per
device**. Zero means unlimited and is the default, so every spec written before
retention existed keeps its behaviour without being edited. A config backup that
silently discards history is not a backup.

It is a property of the type rather than the platform. What makes a capture
worth keeping forever is what it is, not which vendor produced it.

Per (device, type) rather than global, because pruning happens inside one type
directory and a global count would mean one busy device evicting another
device's history.

`Store.Put` takes the count explicitly rather than reading it off a spec — the
store does not import the spec table, and a caller that wants a different bound
for its own type says so.

### Cost

`Cost` is `CostCheap` or `CostExpensive`, and it drives two things.

Within a device, cheap specs run first. A wedged command must not be the reason
a running config was never collected from a session the engine already had open.

Across the run, expensive commands take a **second, smaller concurrency lane**
— default one at a time, fleet-wide. Concurrency five is fine for reading
configs and is not fine for pulling diagnostic bundles off five chassis at once.
That is the "never hang a device" rule biting, and it bites here rather than at
LLDP sizes.

**No shipped type uses the expensive lane today.** The one that did was
`tech-support`, and it was removed. The machinery stays, guarded by a test that
fails if an expensive builtin comes back quietly, because the next command that
needs it will need it during an incident and that is the wrong time to be
rebuilding a concurrency lane.

A spec's own `Timeout` and `MaxBytes` override the session defaults, via
`netexec.RunWith`. Without that, one session carrying both a config and
something far larger has to be sized for the larger, which removes the bound
exactly where it matters. The alternative — a fresh login per capture type —
costs a handshake per type per device and was rejected.

---

## Applicability

"Not applicable" now arrives three ways, and the run model has to keep them
apart because they mean different things to whoever reads the table.

**No command for the platform.** The oldest case: `startup-config` on Junos.
Known before the device is dialed.

**The model gate.** `Command.ModelMatch` is a regex tested against the
fingerprint's version output, which `netexec` already retains. It exists because
a platform key names a NOS and not a chassis: every Junos box answers to
`juniper_junos` whether or not it has a bridge table, so `show
ethernet-switching table brief` would otherwise be sent to every MX in the
estate. The pattern is anchored on a digit — `\b(qfx|ex)[0-9]` — so the word
"ex" in a hostname cannot satisfy it.

**The refusal.** `netexec.LooksLikeRejection`, plus an empty-output check, run
against the output *before* it reaches `Store.Put`. Without them the store
accepts `% Invalid input detected` as a healthy 40-byte artifact, which then
dedups to Unchanged forever and reads as a clean row in every subsequent run.
The rejection check is bounded — a short, few-line output only — so a
configuration that happens to contain the word "invalid" is never mistaken for
a refusal.

### Why Junos is gated and Cisco is not

This asymmetry is deliberate and it is the one design decision the MAC table
forced.

Junos gets a positive `ModelMatch` because the qualifying set is **closed**: QFX
and EX are the switching families, everything else is not, and that list does
not move.

Cisco IOS gets no gate even though it has the same router/switch split, because
the Catalyst model space is **open-ended**. A positive pattern there becomes a
regex edited on every hardware refresh, and its failure mode is the bad one: a
switch the pattern does not recognise is silently reported not-applicable and
never captured again, which is indistinguishable from a device that genuinely
has no such table. So the command is sent and the refusal is caught. A refusal
costs one command and corrects itself the moment the device changes; a missing
pattern does neither.

NX-OS needs neither gate. Every Nexus is a switch.

`Result.Command` is set **after** the model gate rather than before it, so the
two are separable in the table: a model-skipped row carries no command, a
refused row carries the command that was sent. That distinction is the whole
reason to keep the gates as two mechanisms rather than one.

---

## Read-only, provably

Nothing in this product writes to a device. Capture is where that claim is
easiest to make and easiest to get wrong, so it is checked twice.

The first check is an **exact-string allowlist** of every command any spec can
issue. Not a verb pattern: no prefix rule separates Junos `request support
information`, which is read-only, from `request system reboot`, which is not.
Adding a command is deliberately a two-place edit.

The second is an independent word-boundary regex scan. An earlier
`strings.Contains` version matched "format" inside "information" — and a
matcher that crude does not get fixed, it gets edited until it stops
complaining.

That scan carries one documented exception. Junos ARP is read as `show arp
no-resolve`, and a hyphen is a word boundary, so the guard's `\bno\b` pattern
flags it as a configuration command. Dropping the flag is the wrong fix — Junos
then attempts a reverse lookup per ARP entry and a fast read becomes one bounded
by a resolver. The exception names the exact string and the token it excuses, in
the same shape as the existing `set` carve-out, so permitting `no-resolve` does
not permit `no ip routing`.

Both are backed by `internal/fakedev`, whose `Server.Asked()` records what
actually went on the wire. An allowlist over the spec table is an intention;
`Asked()` is evidence.

---

## The store

    <root>/devices/<slug>/<type>/<timestamp>.txt
                         /device.json
                         /<type>/history.jsonl

Capture type is a directory level rather than part of the filename. It reads
the way these get structured by hand, and it makes per-type retention natural —
which the cost question above will eventually need.

Dedup compares against the **last stored** capture, not against any earlier
one, so a config that reverts to a previous state still writes a file. Every
attempt appends a history line, including the ones that skipped the write; a
run that stored nothing and a run that never happened must not look identical.

Same-second captures get a `-N` suffix. Timestamps are
`2006-01-02T15-04-05Z` — no colons, because the store has to survive a
filesystem that dislikes them.

### Pruning

When a type declares a `Keep`, `Put` prunes that one type directory after the
write. Three properties are load-bearing.

**It orders by the history, not by the filename.** Same-second captures are
`<stamp>-1.txt` and `-` sorts before `.`, so a filename sort would delete the
capture it was asked to keep.

**History is rewritten atomically first, then files are unlinked.** A crash
between the two leaves an unreferenced file, which the next sweep removes. The
other order leaves history pointing at files that are gone.

**`Artifact.PruneErr` is non-fatal.** Windows can refuse to unlink a file
something else has open, and a capture that was collected and stored correctly
must not be reported as failed because a retention sweep could not finish. The
next run prunes it.

The history keeps its full record either way. Retention removes artifacts, never
the account of what was collected and when.

A slug collision between two genuinely different canonical names is a **hard
error**. A case-only difference is the same device.

---

## Identity

This is the hard part, and the part with the open problem.

A capture has two names for the same box and they are routinely different:

- the **run identity** — the string the device list used, and the key every
  event in the run is filed under;
- the **canonical name** — what the binding store or the device's own prompt
  says, and the key the artifact is stored under.

Dial `172.16.1.2`, resolve it to `lab-r1.lab.example`, and both are live for
the rest of the visit. Keeping them apart is not cosmetic. Filing capture
events under the canonical while device events use the identity produces one
device as two rows, with the platform column blank because the stamp never
reaches rows keyed under the other string. That bug was in `crawlrun` first and
it was in this engine second; `internal/capture/identity_test.go` is what stops
it being a comment.

### Capture writes to the binding store

Resolve first. On a miss, take the device's own prompt name — first-hand
evidence, the same standard the post-crawl fold applies — and bind it.

Read-only was considered and rejected. A capture run is pointed at a device
list, not at a crawl result, so a device no crawl has ever folded is the
ordinary case rather than an edge case. A read-only engine would file every one
of those under whatever string the list happened to use, and guarantee the
fragmentation the binding store exists to prevent.

Only first-hand evidence is bound: the prompt of a device actually
authenticated to, and the address it answered on. Never a name off a list,
which is a claim rather than an observation.

### The CGNAT rule

An address in 100.64.0.0/10 is reverse resolved, and the PTR name adopted only
if it also resolves forward. Names and ordinary addresses pass through
untouched and cost no lookups.

Shared address space is recycled. Two devices behind different translations can
wear the same 100.64 address inside a month, and a store keyed on that address
files two boxes' configs into one history — silently, with every run reporting
success and the diff turning to nonsense. The identity becomes the confirmed
name; the **dial target stays the address**, because the address is what is
known to answer.

Names are never resolved. A lab whose names have no DNS behind them is a normal
lab, and an unresolvable name is a normal device.

---

## The run model

`internal/capturerun` is the capture-side twin of `crawlrun`, and
`README_Arch_Runs.md` covers the shape in full — events alongside log lines
from one call site, a queryable table instead of a scrolling log, a short
decisions list, `Finish()` resolving anything still in flight.

What differs:

**The row key is a `Pair{Identity, Type}`**, for the reason at the top of this
document.

**Five states, not three.** `Stored`, `Unchanged`, `NotApplicable`, `Failed`,
`Running`. The first three all report `OK()` — nothing is wrong in any of them,
and a summary that treats `Unchanged` as anything else is wrong more often than
it is right, because on a healthy schedule it is the answer for nearly every
device.

**`Unchanged` is deliberately not notable.** It is the expected outcome and it
would drown the decisions list.

**A device that could not be dialed still owes one row per selected type.**
Without that, an unreachable device contributes no rows at all and is
indistinguishable from one nobody asked about — which are precisely the two
things a backup run exists to tell apart. It does *not* owe a decisions-list
entry per type: the device line already said it, and thirteen dead devices
against four types is fifty-two identical lines.

---

## Two front ends, one engine

`capturedial.Build(Params, Options)` is the only way an engine gets assembled,
and it exists before the second front end does rather than after.

The crawl side learned this the expensive way. Two front ends each building
their own crawler drifted, and the symptom was a map with fewer links and
nothing saying which option went missing. A capture has the same shape of
failure available and a worse version of it: a run assembled slightly
differently stores a slightly different set of artifacts, and nobody looks at a
backup until the day they need one.

`Build` lives outside `internal/capture` because opening a vault pulls in
`vaultcli` and the OS keyring behind it, and an engine that cannot be
constructed without a keyring cannot be tested without one. Same reasoning that
keeps a toolkit out of it.

`cmd/capture` is a harness, not a product surface. Every flag maps onto a
`Params` field; it has no behaviour of its own. The order — engine, harness,
UI — is the crawl lesson repeated on purpose: `cmd/crawl` was proven on 83 real
devices before `crawlrun` existed, so when the window found a bug it was
identifiably a window bug.

---

## Parameters

`capturerun.Params` is the modal's model, validated without a toolkit.
Devices and capture types are two independent lists and the run is their cross
product.

Per-device type overrides were considered and rejected. The row key already
assumes the cross product, and an override list would make the third outcome
ambiguous: a blank cell would mean either "this platform has no command for
this type" or "somebody deselected it", and `NotApplicable` stops meaning
anything.

`Types` is a list of strings rather than specs, and that is forced rather than
stylistic — `internal/capture` imports `capturerun`, so `capturerun` can never
import `capture`. The string-to-spec lookup therefore happens in `Build`, which
is the one place both front ends already go through. `Validate()` checks shape;
`ValidateAgainst(known)` checks the names against whatever the caller knows.

Host keys default to **Strict**, deliberately the opposite of the crawl
default. A crawl meets devices it has never seen, so TOFU is the normal case
there. A capture works from a list of devices someone already administers.
Insecure is offered by neither: it is the only mode that also stops noticing a
key that *changed*.

The device-list parser honours `#` comments. The commented-out line explaining
why a box is off the list is the line that stops it being added back.

---

## Known gaps

**The binding store can fork, and capture is what makes it visible.** Alias
matching is exact-string, so a record canonicalized as `lab-r1.lab.example`
and one as `lab-r1` never merge — even though the store file records its own
suffix context and therefore knows they are the same device. A capture run
that resolves by address, misses, and names the device from its prompt will
create a second record rather than enriching the first. The fix belongs in
`credres`: alias matching should be suffix-aware against the recorded suffix
list. Until then a device whose bound aliases do not include the address the
list dials can fork on every run.

**A seed device carries less identity than the devices behind it.** The same
weakness Secure Cartography had, arriving here for the same reason: every
non-seed device is described twice — once by the neighbor that reported it,
once by itself — while a seed is described only by itself. The neighbor's
report is what supplies the management address and the qualified name, so a
seed can end up in the store with a single alias and no address, which is
exactly the record shape that later forks. Not urgent, and not a capture bug;
it is where the identity pipeline is thinnest and it is worth knowing before
reading a strange-looking store.

**There is no CLI for the binding store.** Three things write it — `credres`
on a successful authentication, `crawldial.Fold` after a crawl, and
`capture.identify` — and nothing reads it. Inspecting or repairing it means
hand-editing JSON that three processes write. Inspect, merge and forget
subcommands are the missing tool, and they matter more than the matching fix:
one stops new forks, the other is how you find out forks are happening.

**Credential outcomes do not reach the capture run model.**
`credres.Config.Emit` is typed `crawlrun.Emit`, and capture's events are a
different type, so `AuthOK`, `AuthReject` and `CredParked` are invisible here.
The two `HostKeyEmitter` functions differ only in which event type they build,
and a shared credential-setup helper could not carry an emitter either. All
three trace to one decision that has not been made: whether `crawlrun` and
`capturerun` should share an event type. The row models genuinely differ — a
crawl row is a device, a capture row is a pair — but the event kinds and the
emit plumbing are the same shape.

**No device entry anywhere can carry a port.** `dial.BaseConfig.Apply` sets
only `sshcore.Config.Host`, `sshcore.Dial` joins that with a `Port` that
defaults to 22, and the target shape check rejects a `:`. Affects crawl
equally. It matters for containerlab, where devices are routinely
port-forwarded onto localhost.

**Agent authentication is unreachable.** `sshcore` implements it — the auth
chain is agent, key, password, keyboard-interactive — but `dial.Static` never
sets `UseAgent`, so neither CLI can use it.

**Search over the bounded types is literal only.** `storesearch` scopes a search
to a type, so "which port is this MAC on" is already answerable from the store —
but the matcher compares bytes, and a MAC address is written `0011.2233.4455`,
`00:11:22:33:44:55` or `00-11-22-33-44-55` depending on who printed it. One
query therefore searches one vendor's spelling. A format-aware matcher for MAC,
IP and ASN is the scoped fix and the `Matcher` seam exists for it; until then
the honest description of MAC search is that it works if you type the address
the way the device does.

**The run table's shape assumes Unchanged is the common outcome.** True for the
first three types, and inverted for the last two: `arp-table` and `mac-table`
differ on nearly every read, so a run with them selected is mostly Stored. The
counters are still correct — nothing is miscounted — but the visual weighting
was chosen when Stored meant "something happened", and it no longer only means
that.

**Diffing is not here.** The store keeps raw text and content hashes; it does
not compare captures. That is deliberate for v1 and it is the line that keeps
this from becoming a different product.

**`gotextfsm` compiles its regexes per line, per parse.** Irrelevant at LLDP
sizes. It will not be irrelevant the first time captured running-configs are
fed through it.