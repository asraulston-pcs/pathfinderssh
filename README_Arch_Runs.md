# Run Architecture

How PathfinderSSH turns a crawl into something you can still interrogate
tomorrow.

    internal/crawlrun   events, the run model, parameters, run-to-run diff
    internal/ui         the Fyne view over it
    cmd/crawlui         the window

`internal/crawlrun` imports no toolkit and does not import the crawler. That is
the load-bearing constraint of this whole package: everything that answers
"what happened" is testable without a display, and a second front end — a
different toolkit, a CLI summary, a web export — costs nothing extra.

---

## The problem this solves

A log is only useful while it is scrolling.

Every question worth asking about a crawl is asked afterwards. Which devices
were never dialed. Which one needed the address fallback. Which credential won
where, and which device paid for a ladder walk. Who reported that host you have
never heard of. A scrolling log cannot answer any of them, because answering
them means holding state and a log holds none.

That difference is the whole line between an application and a script runner.
A script runner forgets. An application holds state between runs — which is
also why the binding store exists, and why it mattered there for the same
reason it matters here: forgetting is not free, it is re-paid every run.

---

## Events

The crawler and the credential resolver emit `crawlrun.Event` alongside their
log lines. The kinds are not invented; each one already existed as a distinct
log message, and this package only gave them a shape.

    lifecycle   Queued Depth Platform Reached Failed NotDialed
    identity    Resolved RetryAddr Renamed
    credentials AuthOK AuthReject CredParked
    host keys   HostKeyNew
    collection  Collect CollectErr

Two fields carry more weight than their size suggests.

**`CredReason`** is credres's own ranking word — `pinned`, `promoted`, or
`ranked`. Without it an attempt count is a number; with it, the number means
something. `pinned` says the binding store hit and no failed logins happened.
`ranked` says it missed and the ladder was walked, and every attempt before the
last was a real authentication failure against a real account.

**`Via`** is the identity of the device whose neighbor table produced this one,
empty for a seed. It answers the first question an unexpected row provokes —
which box reported this — that otherwise means correlating log lines by hand.
It matters most on devices that are never dialed, because for those it is the
only thing that will ever be learned.

---

## The run model

`Run` folds events into `DeviceRow`s, counters, and a short decisions list. It
is safe for concurrent use, because a crawl emits from every worker while a
view reads on the main thread.

### Three outcomes, not two

    Reached     connected, collected, done
    Failed      dial, session, or fingerprint failed
    Not dialed  deliberately never connected to

The third one is why this is a table and not a progress bar. A device excluded
by pattern, or sitting outside `AllowDomains`, is drawn into the map as a leaf
— indistinguishable from a genuine edge device — and in a log its only trace is
one line that has already scrolled away. Collapsing it into either success or
failure is how it becomes invisible. As a counter you can click into, it stops
being invisible.

The view styles it italic rather than red for the same reason: if it reads as a
failure, the distinction the counters exist to draw is undone by the styling.

### Attempts

`DeviceRow.Attempts` counts only `AuthReject` and the success that ended the
walk. A host-key or reachability failure would reproduce identically for every
credential and says nothing about any one of them, so it does not count.

`Counts.AttemptsPerReached` is the run's cost in failed logins. A warm binding
store holds it near 1.0. If it climbs between runs, the bindings stopped
matching — and the comparison below names the devices.

### Finish is not optional

`Run.Finish()` resolves anything still Queued or Running into a failure with a
reason. A row that reads "running" forever is the same silent gap the rest of
this package exists to close.

If a row ends up carrying *"run ended before this device completed"*, that is
this sweep, and it means no terminal event ever arrived for that device. It is
a wiring bug, not a timeout.

### Decisions

A filtered view of `Event.Notable()` — the non-default outcomes only. Not
"errors": a device silently not dialed is not an error and is the single most
important thing to surface, while a successful authentication is only
interesting when the pin missed and the ladder had to be walked.

A healthy crawl should produce very few entries. Anything that fires on every
row does not belong here — a rename that changes nothing was removed for
exactly that reason. A decisions list that fills with no-ops stops being read.

---

## Comparison

`Snapshot` saves a finished run; `Compare` diffs the previous one against the
current rows.

    Appeared       found this run, not last
    Vanished       known last run, never seen this one
    StateMoved     same device, different outcome
    PlatformMoved  fingerprint changed underneath you
    LadderCost     spending more credential attempts than it used to

`LadderCost` is the interesting one. A device that suddenly needs three
attempts instead of one usually means its binding stopped matching, not that
anything about the device changed.

The diff keys on `Identity`, never on the display name. A label that moves
between runs turns every diff into noise and makes a real change
indistinguishable from the labelling wobbling — the same reason the binding
store derives its canonical label deterministically rather than from whichever
call supplied it.

`StateMoved` is worth reading carefully: a device that went from reached to
not-dialed did not break. A policy changed.

---

## Parameters

`crawlrun.Params` is the model both front ends fill in — the CLI from flags,
the window from widgets — and `Defaults()` mirrors the CLI flag defaults
exactly so the two cannot drift into producing different crawls from the same
intent.

- `ParseSeeds` splits a free-text field on newlines, commas, spaces or
  semicolons, because a paste out of a spreadsheet or a ticket uses any of them.
- `Validate` returns **every** problem with a field name attached, so a form can
  mark all the offending inputs in one pass rather than one per submit.
- `Normalize` is safe to call as the user types; it makes `.lab.local` and
  `lab.local` the same intent.

Seed validation is **shape only, never resolution**. A name that does not
resolve is still a legitimate seed — a lab with no DNS behind its domain is the
normal case, and the address fallback is what covers it.

### Host keys

`Params` offers `tofu` (default) and `strict`. It does not offer insecure.

Discovery meets devices it has never seen, so an unknown key is the ordinary
case and TOFU accepts it, records it, and moves on. A key that *changed* is a
different event entirely and fails closed in `sshcore` regardless. Insecure is
the only mode that also stops checking for the second thing, and skipping that
is a decision someone should have to type on a command line rather than tick in
a dialog next to the concurrency box. `Validate` rejects it with that reason.

TOFU acceptances emit `HostKeyNew` and are counted, which is what makes
trust-on-first-use auditable rather than blind: you are not claiming you knew
the keys, you are recording what you saw. Large on a first crawl of an estate,
near zero afterwards, and a later run that jumps is worth a look.

### Profiles

Named parameter sets, atomic writes, most-recently-used first. Without them a
dialog opens empty every time and filling a form is slower than the CLI it
replaced, which is the script-runner failure wearing a nicer hat.

---

## The Fyne layer

`internal/ui/crawlview.go` renders a `Run`. Everything above this line is
toolkit-free; that part is not, and it has lifecycle and threading constraints
that are easy to get wrong and produce panics naming the wrong thing.

Those are in **README_Fyne_UI.md**, along with the conventions the rest of the
UI work should follow. They are not repeated here — one place per subject, for
the same reason the parameters live in one struct.

The one point that belongs on this side: the table is the primary surface, not
a text pane. The log still exists; it moved to where it is the answer to a
question rather than the whole interface.

---

## Known gaps

**Credential outcomes reach the row, not the map.** `Cred`, `Try` and
tries-per-device come from `credres`; nothing correlates them back into
`map.json`.

**Nothing routes on `Via` yet.** The parentage is carried and displayed. The
jump-host `inherit` case that wanted it is still unwired.

**The comparison tab needs a saved run.** `-save-run` writes one; without it the
tab is empty on a first run and there is no prompt saying so.

**`crawlview.go` has no tests.** Same defensible reason as `sshcore`: the honest
test drives a display. The run model underneath it is covered instead, which is
why the split exists.
