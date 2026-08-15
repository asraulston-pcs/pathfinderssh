# Credential Architecture

How PathfinderSSH stores credentials, decides which one to offer a device, and
learns from what happens.

Four packages and one command:

    internal/vault      secrets at rest
    internal/credres    which credential for this device, and in what order
    internal/vaultcli   where the master password comes from
    cmd/crawl/dialer.go applying a credential to a connection
    cmd/pfvault         creating and managing the vault

---

## The problem this solves

A terminal and a crawler want different things from a credential store.

A terminal session is one device the operator chose, with one credential the
operator picked. The store is a convenience: it saves typing.

A crawl is different in every dimension that matters. It reaches devices nobody
selected — whatever LLDP happened to report. It reaches a lot of them. And it
has no idea which credential any of them wants until it tries.

That difference is not about features. It is about failure. On a terminal, a
wrong credential is one rejected login and a shrug. On a crawl, a wrong
credential is one rejected login *per device*, against the same account, as
fast as the concurrency allows. A fabric of two hundred devices turns a single
stale vault entry into two hundred consecutive authentication failures on one
account, which is an account lockout and possibly a page for whoever owns the
directory.

**Lockout is the governing threat in this subsystem.** Most of the design below
is a consequence of it.

---

## Layers

    ┌──────────────────────────────────────────────────────────┐
    │ cmd/pfvault            init, add, list, rm, keyring      │
    │ cmd/crawl              -vault, -cred-tag, -max-creds     │
    └───────────────┬──────────────────────────────────────────┘
                    │
    ┌───────────────▼──────────────────────────────────────────┐
    │ internal/vaultcli      master password: keyring/env/tty  │
    │                        Open() = unlock, with fallback    │
    └───────────────┬──────────────────────────────────────────┘
                    │
    ┌───────────────▼──────────────────────────────────────────┐
    │ internal/credres       ordering, learning, restraint     │
    │                        Resolve / Walk / Report           │
    └───────────────┬──────────────────────────────────────────┘
                    │  Store interface: All() []Credential
    ┌───────────────▼──────────────────────────────────────────┐
    │ internal/vault         Argon2id + AES-256-GCM at rest    │
    │                        Unlock / All / Add / Update / Del │
    └──────────────────────────────────────────────────────────┘

The dependency direction is the important part. `vault` knows about secrets and
nothing about hosts. `credres` knows about hosts and treats secrets as opaque
blobs it never inspects. Neither knows anything about SSH — the dial layer in
`cmd/crawl` is where a credential becomes an `sshcore.Config`, and it is the
only place that mapping exists.

`credres` sees the vault through a one-method interface:

```go
type Store interface {
    All() ([]vault.Credential, error)
}
```

Narrow on purpose. The resolver never creates, updates, unlocks, or deletes
anything, and the type system says so. It also means the resolver is trivially
testable against a slice of credentials with no vault file and no Argon2 work.

---

## internal/vault — secrets at rest

Ported from the TetherSSH terminal's credential vault. The **on-disk format is
unchanged**: Argon2id key derivation, AES-256-GCM sealing, same JSON envelope,
and the legacy application tag is accepted on read. An existing terminal vault
opens here without migration, and the next write re-seals it under the current
tag.

What the port removed: the session model and every reference to the terminal's
UI. What it added, all of it non-secret metadata that lets a resolver order
candidates without a second store:

| Field         | Purpose                                                        |
|---------------|----------------------------------------------------------------|
| `Priority`    | Ordering within the same scope specificity. Lower runs first.  |
| `Tags`        | Free-form selectors. A caller may require *all* of them.        |
| `Scope`       | Eligibility: domain suffix, CIDRs, platforms.                   |
| `Description` | Free text for a picker UI. Never parsed.                        |
| `Disabled`    | Out of automatic selection without being deleted.               |
| `IsDefault`   | What an empty credential reference resolves to. At most one.    |

`Scope` deserves a note. A zero scope is unrestricted. A populated scope must
match on **every** populated field, and `Scope.Specificity()` scores how narrow
it is: an address match outranks a platform match, which outranks a domain
suffix, which is itself weighted by label count so `iad.lab.example.net`
outranks `lab.example.net`. Specificity, not priority, is the primary sort —
priority breaks ties within a specificity band.

That ordering is deliberate. A credential scoped to one rack should be tried
before a fabric-wide fallback regardless of what numbers someone typed into the
priority fields, because the narrower statement is the more informed one.

### The default credential

`SetDefault` / `Default()` / `ClearDefault()` / `DefaultName()` answer one
question: what does a session mean when it names no credential at all? Without
an answer, an estate imported from a map is a hundred nodes each needing the
same edit.

An empty `Node.Credential` **asks the store what it uses when nothing is
named**, and the blank stays blank in the session file. Writing the resolved
name into each node would turn changing the estate's credential back into an
edit per device, which is the problem the default exists to remove.

Three rules, each of which had a reason:

- **All-or-nothing.** The default fills a session that states no auth of its own
  — no username, no password, no key path — and stays out of one that does.
  Merging field by field yields a credential nobody assembled. It is also the
  only route back to manual auth: typing a username is how a session opts out.
- **A named credential outranks it.** Naming one is a choice. The default is
  what happens without a choice.
- **Session only, never the jump host.** `resolveWithDefault` is used for the
  node and plain `resolve` for the bastion, because authenticating to somebody's
  jump host with the estate default is a decision nobody made.

`Default()` skips a disabled credential — disabled means out of automatic
selection, and nothing is more automatic than being the default. It stays
reachable by `Get`, which is what disable is meant to leave working.

`AuthType` **cannot** be used to detect "the session said nothing." `Normalize()`
fills an empty one with `agent`, so a deliberate agent choice and an unset field
are the same value by the time anything reads them, and every map-imported node
carries `agent`.

One consequence reached further than expected. `Node.Validate()` decides the
"username required unless a vault credential supplies one" rule on
`Credential != ""`, so an imported node — no username, no credential — was
rejected before the default was ever consulted. Hence
`ValidateFor(credentialDefault bool)` / `ErrFor(bool)`, with `Validate()` and
`Err()` delegating with `false` so nothing else changes. The same flag defers the
public-key `key_path` rule, since a default can supply a key path exactly as it
supplies a username. A post-lookup check in `connectSSH` then catches the real
gap where the answer is finally known: *no username: the session names none, and
no credential supplied one.*

---

## internal/credres — the ordered list

The core question: given "I am about to dial this device," produce an ordered
list of credentials to try.

```go
type Target struct {
    Identity string   // canonical cache key — see "Identity" below
    Addr     string   // literal address when known, for CIDR scope matching
    Platform string   // fingerprint result; empty before the connection exists
    Tags     []string // caller's requirement: a credential must carry all
}
```

`Resolve(Target) []Candidate` filters and orders. `Walk(Target, dial)` resolves
and then tries each in turn until one authenticates. `Report(target, credID,
outcome)` feeds the learning.

### Four mechanisms

**Pin.** The persisted last-known-good credential for this identity goes first,
and survives across runs. It is a hint, not an authority: if the pin fails to
authenticate it is dropped and the full ladder is walked. A pin that hardened
into a rule would mean a credential rotation bricks every device until someone
deletes a cache file.

**Promotion.** Within one run, the credential that most recently authenticated
*anywhere* is tried first on the next unknown identity. This is what makes a
cold crawl fast. On a first pass the persisted pins are empty, and most devices
in a fabric share a credential set — so the second device onward starts with
the answer that just worked.

**Negative cache.** A credential rejected by this identity during this run is
not offered to it again. Prevents a retry loop from re-spending attempts
against a device that already said no.

**Circuit breaker.** A credential rejected by *N distinct identities* in a row
is parked for the rest of the run. Default N is 3.

The breaker is the lockout guard, and it is the mechanism this subsystem exists
for. "Distinct identities" is the load-bearing phrase: three rejections from one
device is a device problem, three rejections from three different devices is a
credential problem, and only the second pattern should stop the credential
being offered anywhere else.

`MaxPerHost` (default 4) is an independent second guard — a cap on how many
credentials any one device is offered, regardless of what the other mechanisms
think. Two guards on different axes, because they fail differently: the breaker
protects an account across the fabric, the cap protects a device from being
hammered.

### Only one outcome feeds any of this

```go
func (o Outcome) CountsAgainstCredential() bool { return o == OutcomeAuthRejected }
```

A handshake that failed on algorithm negotiation says nothing about the
credential. Neither does a host-key mismatch, a refused connection, or an
unreadable key file. If those fed the breaker, one legacy-crypto device would
convince the resolver that every credential in the vault is bad.

`Retryable()` is the other half: after a host-key failure or an unreachable
device, walking to the next credential is pure lockout exposure with no chance
of success, because every remaining credential fails identically. The ladder
stops.

    Outcome            Counts against cred?   Keep walking?
    ─────────────────  ────────────────────   ─────────────
    Success            —                      —
    AuthRejected       yes                    yes
    KeyMaterial        no                     yes
    AlgoMismatch       no                     no
    HostKey            no                     no
    Unreachable        no                     no
    Other              no                     no

### Classification is string matching, and that is a known cost

`golang.org/x/crypto/ssh` does not export typed errors for most handshake
failures, so `Classify` matches on error text. Two vocabularies arrive: x/crypto
produces the handshake and authentication text, and `sshcore` wraps that but
also emits its own for failures it detects locally — an unreadable key file, a
declined host key, a jump host with nothing to authenticate with.

The second set was missing initially and those errors fell through to
`OutcomeOther`, which is non-retryable and counts against nothing. The visible
symptom was a ladder that stopped on the first device with a misconfigured key
and never said why.

`sshcore_errors_test.go` pins both vocabularies. Three of the sshcore cases are
generated by calling `sshcore.Dial` for real with configurations that fail
while building auth methods or the host-key callback — both of which happen
before the TCP connect, so the test needs no network and the strings cannot
drift silently. The rest need a live server and are a literal table annotated
with the source line each came from.

---

## Identity — the key everything caches on

Every cache in `credres` is keyed on a canonical identity rather than on
whatever string happened to be dialed.

The rule lives in `internal/normalize/identity.go`, and there is exactly one
implementation of it. It used to exist three times — in the crawler's dial
path, in the `reach` CLI, and here — and two of the three disagreed.

```go
normalize.Canonical(host, suffixes)
```

- Lowercase, trim trailing dot, strip any configured domain suffix.
- An address inside `100.64.0.0/10` (RFC 6598) is not a stable identity: the
  same address is reused at every site, so two different devices would collide
  on one key and inherit each other's bindings. Those get a reverse lookup.
- **The PTR is forward-confirmed.** A name that does not resolve back is a
  stale reverse record, and dialing it fails where the address would have
  connected. Unconfirmed, the address is kept.

The divergence that motivated the consolidation was exactly this last point:
`credres` took the first PTR unconditionally where the crawler forward-confirmed
it. A device with stale reverse DNS keyed on the name in one place and on the
address in the other — one device, two cache entries, no error anywhere. The
cache warms twice and helps neither path.

That failure mode is why identity gets its own package and its own test file
rather than living wherever it was first needed. Nothing about it is loud.

**Inside a crawl, do not call `credres.Identity`.** The crawler already
resolved the device and claimed it under a key, and passes that through as
`crawler.DialTarget.Identity`. Recomputing invites a second DNS answer and a
second key. `Identity` is for callers holding nothing but a hostname.

---

## Applying a credential — cmd/crawl/dialer.go

`credres.Walk` *wraps* `sshcore.Dial` rather than replacing it. Each rung of
the ladder is one ordinary dial with different credential fields layered on:

```go
_, err := d.res.Walk(target, func(c vault.Credential) error {
    cfg := d.base.apply(t.Target)   // algorithms, host-key policy, bastion
    applyCredential(&cfg, c)        // username + the material this type uses
    cl, derr := dialWithLegacyFallback(d.dialFn(), cfg, d.legacyFallback, d.logf)
    if derr != nil {
        return derr
    }
    client = cl
    return nil
})
```

`baseConfig` holds what does not vary per device or per credential: algorithm
policy, host-key policy, the bastion, and where discovered host keys are
written. Everything credential-shaped is layered on top.

`applyCredential` sets fields **strictly by the credential's declared auth
type** — password, or key plus passphrase, or agent. Not "set every non-empty
field." A credential carrying both a key and a password would otherwise put two
authentication attempts on the wire per ladder rung, which doubles the lockout
exposure of that credential across the whole fabric for no gain.

Without `-vault`, `staticDialer` applies one credential from the command line
to every device. That is the original behavior and remains the right thing for
a single-credential lab.

---

## The other retry axis

`OutcomeAlgoMismatch` says the retry axis is the algorithm set, not the
credential — and for a long time nothing acted on that. The only lever was
`-legacy`, a global flag that offers weak algorithms to every device in the
crawl.

That is the wrong instrument. One twenty-year-old console server offering
nothing but `diffie-hellman-group1-sha1` would force every edge router in the
same run to be dialed with a weakened algorithm set.

`dialWithLegacyFallback` retries **the same credential, once, on the same
device**, with `LegacyAlgorithms` enabled — and only after that device has
proved it can do nothing better. Weak algorithms stay off every other dial.
The downgrade is logged to stderr unconditionally, not gated behind `-v`: an
operator should not have to be in verbose mode to find out a device was reached
with weak crypto. `-legacy-fallback=false` turns it off.

---

## Unlock — settled

The vault is master-password-based, which is what a terminal needs and what a
headless crawl cannot supply. The resolution order, strongest available first,
lives in `vaultcli.Master()`:

1. **OS keyring.** Windows Credential Manager, macOS Keychain, Linux Secret
   Service, via `github.com/zalando/go-keyring` (MIT). Entries are keyed by
   the **absolute vault path**, so a lab vault and any other vault on the same
   machine never share one.
2. **`PATHFINDER_VAULT_PASSWORD`.** Still supported, because a headless Linux
   box with no D-Bus session has no keyring to reach and a scheduled crawl
   still has to run. It is a plaintext path by another name and it is last for
   that reason.
3. **Interactive prompt.** TTY without echo, or one line from a pipe.

The vault file format did not change. Argon2id still derives the AES key from
the master password; the keyring only answers where the master came from. An
earlier sketch had the keyring hold a 32-byte wrapping key with the master
sealed under it in a sidecar file — dropped, because both artifacts live on
the same machine under the same user, so anything that can read one can read
the other and the indirection only adds a file that can go missing.

Three decisions worth stating, because each is a place this could have gone
quietly wrong:

- **The keyring outranks the environment variable.** An entry the operator
  filed on purpose should not be shadowed by a variable inherited from a
  parent process. To stop using a stored entry, clear it or set
  `PATHFINDER_NO_KEYRING` for the run — both explicit, which is the point.
- **A stale entry never locks anyone out.** If a keyring-sourced master fails
  with `ErrWrongPassword`, `Open()` says so and re-prompts. Any other failure
  is returned as-is: a mistyped prompt is the human's to repeat, and a wrong
  environment variable is a scripting bug that should surface.
- **`MasterNew()` does not consult the keyring.** An entry left behind by a
  deleted vault must never silently become the master password of a new one.

`pfvault keyring set` verifies the password by unlocking the vault before
filing it. Storing an unverified string is exactly how a keyring entry becomes
a lockout.

`vaultcli` exists as its own package for this reason, and `vault` itself stays
free of terminal coupling — which was the point of the port. Putting the
prompt back inside `vault` would undo it.

---

## cmd/pfvault

The vault lives at `~/.pathfinderssh/vault.json` unless `-vault` says
otherwise. That directory is the one `ui.AppHomeDir` uses; a vault at the older
`~/.pathfinder/vault.json` is still found when nothing exists at the current
location, so an existing install is not orphaned by the correction.

```
pfvault init
pfvault add -name lab-key -user admin -key ~/.ssh/id_ed25519 -tag lab -priority 10
pfvault add -name lab-pw  -user admin -tag lab -priority 20
pfvault list
pfvault default lab-key  # what an empty credential reference resolves to
pfvault default          # bare form REPORTS; it never changes anything
pfvault default -clear
pfvault keyring set      # store the master password, so a crawl needs no human
pfvault keyring status   # what the unlock path would find
pfvault keyring clear
```

**No secret is ever a command-line argument.** argv is visible in the process
table and lands in shell history and in whatever shell-integration log the
terminal keeps. Secrets are prompted on a TTY, or read as a line from stdin
when piped, so scripted use is `echo "$PW" | pfvault add ...` and never
`pfvault add -password "$PW"`.

`add` collects only the material the declared auth type will actually use,
matching how the dialer applies it. Storing a password on a publickey
credential means carrying a secret nothing ever reads.

`default` with no argument **reports** rather than clearing. A command whose
bare form is destructive is one typo from a bad afternoon. `-default` on `add`
sets it at creation, and setting a disabled credential is refused with "enable
it first."

One operational note that has already caused confusion: `-cred-tag` requires a
credential to carry **all** the tags passed. Two credentials with different tags
have no single `-cred-tag` value that selects both, and a ladder test needs the
flag omitted entirely.

---

## What is not built

- **Platform-scoped credentials in a crawl.** `Target.Platform` is empty from
  the crawler, because the fingerprint does not exist until the connection is
  up and the neighbor claim that carries a platform hint is dropped at enqueue.
  Platform-scoped credentials are therefore skipped rather than guessed at —
  correct behavior, but the scope is inert on the crawl path until the BFS
  queue carries the claim.
- **Jump-path integration.** `internal/jump` resolves a bastion path per device
  and can bind credentials to hops, but nothing in the crawl consults it yet;
  the bastion is still whatever `-jump` says. Multi-hop dialing does not exist
  in `sshcore` — it does single-hop today — and that is the one piece of real
  new work.
- **Credential rotation / expiry.** The vault has `LastUsed` and nothing reads
  it. Declined for v1; see `DEFERRED.md`.

Both of the first two items above are one root cause — the BFS queue not
carrying the neighbor claim — and both are tracked with triggers in
`DEFERRED.md`.