# Quickstart

Vault setup, then one worked example for each `cmd/` binary. Worked against a
dynamips lab reachable at `192.168.100.2` with hostnames under `local.lab` —
substitute your own seed and domain.

Four binaries:

    cmd/pfvault    credential vault management
    cmd/reach      run commands on one device, non-interactive
    cmd/crawl      walk the topology from a seed, write map.json
    cmd/pfterm     interactive terminal (SSH / telnet / serial)

---

## 0. Build

```sh
go build ./cmd/pfvault ./cmd/reach ./cmd/crawl
go build ./cmd/pfterm          # needs cgo + a C toolchain + a display
```

`pfterm` is the only one that pulls Fyne. The other three are pure Go and will
build anywhere.

If `pfterm` starts and renders nothing on Crostini, the usual cause is the
swrast fallback rather than the app:

```sh
LIBGL_ALWAYS_SOFTWARE=1 ./pfterm -ssh admin@192.168.100.2 -legacy
```

---

## 1. Vault setup

The vault lives at `~/.pathfinderssh/vault.json` by default. `init` creates the
directory.

```sh
./pfvault init
# new vault master password:
# confirm master password:
# created /home/you/.pathfinderssh/vault.json
```

Minimum 8 characters. The master password is never written anywhere — Argon2id
derives the AES-256 key from it at unlock, and a wrong password is detected by
GCM authentication failure rather than by a stored hash.

`pfvault init` is not the only route: `cmd/pathfinder` offers to create a vault
on first run when none exists, and its Vault menu offers again whenever the file
is missing. Declining is a legitimate answer — a session can carry its own
credentials, and a crawl or capture accepts a static username and password from
its launch form. What a vault buys is unattended work, where each device is
resolved against stored credentials instead of one password typed by hand.

### Add credentials

Two credentials make a ladder: a key tried first, a password as fallback.

```sh
./pfvault add -name lab-key -user admin -key ~/.ssh/id_ed25519 -tag lab -priority 10
./pfvault add -name lab-pw  -user admin                        -tag lab -priority 20
# password for admin@lab-pw:
```

Lower priority runs first. **Give both credentials the same tag.** `-cred-tag`
requires a credential to carry *all* the tags passed, so two credentials with
different tags have no single `-cred-tag` value that selects both — the second
rung is silently never offered. This has bitten before.

No secret is ever a command-line argument. Everything is prompted, or read as
one line from a pipe:

```sh
echo "$PW" | ./pfvault add -name lab-pw -user admin -tag lab -priority 20
```

### Check it

```sh
./pfvault list
```

Prints NAME, USER, AUTH, PRIO, TAGS, SCOPE, STATE. Never secrets.

### A default credential

A session that names no credential asks the vault what it uses when nothing is
named. That is the answer to "I have a hundred sessions and I do not want to
edit each one" — particularly after a map import, where every node arrives with
no credential at all.

```sh
./pfvault default lab-key    # set it
./pfvault default            # report it — the bare form never changes anything
./pfvault default -clear     # back to no default
```

`add -default` does both in one step. `list` marks the default in STATE.

Two rules decide when it applies, and both are deliberate:

**The default fills a session that states no auth of its own, and stays out of
one that does.** Any of a username, a password or a key path counts as stating
its own. It is all-or-nothing, never field-by-field: merging would produce a
username from one place and a password from another, a credential nobody
assembled and nobody can debug from the screen. This is also the way back —
**typing a username is how a session opts out.**

**A credential named on the session still wins.** Naming one is a choice; the
default is what happens in the absence of a choice, so it loses to anything the
session says.

The default applies to the session credential only, never to a jump host.
Silently authenticating to somebody's bastion with the estate default is a
decision nobody made.

A disabled credential is skipped, and `default` refuses to set one — disabled
means out of automatic selection, and being the default is the most automatic
there is. It stays fetchable by name, which is what disable is meant to leave
working.

### Optional scoping

A credential can be pinned to where it applies, so the ladder does not offer a
datacenter credential to a lab device:

```sh
./pfvault add -name lab-only -user admin -tag lab -priority 10 \
  -scope-cidr 192.168.100.0/24 -scope-domain local.lab
```

Note that `-scope-platform` is currently inert on the crawl path — the
fingerprint does not exist until the connection is up, and the neighbor claim
that would supply a pre-dial hint is dropped at enqueue. Tracked in
`DEFERRED.md`.

### Keyring (and what to expect on Crostini)

```sh
./pfvault keyring status
```

On macOS and Windows this should report `available, no entry`, and:

```sh
./pfvault keyring set      # prompts, unlocks the vault to verify, then files it
```

means `crawl -vault` runs with no human present.

**On Crostini, expect `state  unavailable`.** The Linux backend is the D-Bus
Secret Service, and the base container has no keyring daemon and often no user
D-Bus session. Nothing breaks — `Master()` falls through to the environment
variable and then to a prompt, and prints a one-line note on stderr. For
scripted crawls on this box:

```sh
export PATHFINDER_VAULT_PASSWORD='...'
```

That is a plaintext path by another name, which is why it is last in the
preference order. Use it on the Chromebook, not on anything that matters.

If you want to try the real thing: `sudo apt install gnome-keyring`, then run
under `dbus-run-session -- ./pfvault keyring status`. It is fiddly and it is
not a prerequisite for anything.

To take the keyring out of the loop for one run without destroying the entry:

```sh
PATHFINDER_NO_KEYRING=1 ./pfvault keyring status
```

---

## 2. cmd/reach — one device, non-interactive

Fingerprint only. The cheapest possible proof that transport, auth and prompt
detection all work:

```sh
./reach -host 192.168.100.2 -user admin -p -legacy -fingerprint-only
```

Run commands. Each `-c` is exactly one command — there is no comma splitting:

```sh
./reach -host 192.168.100.2 -user admin -p -legacy -fingerprint \
  -c "show version" \
  -c "show lldp neighbors"
```

`-fingerprint` detects the platform and picks the paging-disable command for
you. Set it explicitly instead if you would rather not probe:

```sh
./reach -host 192.168.100.2 -user admin -p -legacy \
  -paging "terminal length 0" -c "show ip interface brief"
```

Through a jump host — the path you actually use to reach the containerlab box:

```sh
./reach -host 192.168.255.1 -user admin -p \
  -jump you@jumphost -jump-key ~/.ssh/id_ed25519 \
  -fingerprint -c "show ip ospf neighbor"
```

Useful flags: `-agent` (on by default, tried first), `-hostkey strict|tofu|insecure`,
`-enable` for privileged mode, `-timeout`.

`-legacy` widens KEX/cipher/MAC negotiation for old gear. A dynamips c7200 will
need it; cEOS will not.

---

## 3. cmd/crawl — walk the topology

Simplest form, using the vault:

```sh
./crawl -seed 192.168.100.2 -vault ~/.pathfinderssh/vault.json \
  -domain local.lab -depth 3 -o lab-map.json -v
```

`-domain` does double duty: it is stripped from node names in the map, and
appended when resolving bare neighbor names. `-depth 0` crawls the seeds only.

Keep the crawl inside the lab and let everything else map as an undialed leaf:

```sh
./crawl -seed 192.168.100.2 -vault ~/.pathfinderssh/vault.json \
  -domain local.lab -allow-domain local.lab \
  -depth 4 -concurrency 5 -o lab-map.json -v
```

Without a vault, single credentials work too:

```sh
./crawl -seed 192.168.100.2 -user admin -password -legacy \
  -domain local.lab -o lab-map.json -v
```

Keep discovery's host keys out of your personal `known_hosts` — first contact
auto-accepts and persists, and you do not want that mixed in with keys you
verified by hand:

```sh
./crawl -seed 192.168.100.2 -vault ~/.pathfinderssh/vault.json \
  -known-hosts ~/.pathfinderssh/discovery_known_hosts \
  -domain local.lab -o lab-map.json -v
```

**Do not pass `-cred-tag` on a first run.** See the tag note in the vault
section — it is the fastest way to make a working ladder look broken.

Credential-resolution flags worth knowing: `-max-creds` caps attempts per
device, `-cred-breaker` parks a credential after it is rejected by that many
distinct devices (a lockout guard, not an optimization). `-exclude` takes
substrings matched against platform, hostname and sysname.

Output is `map.json` in the same shape Secure Cartography emits, so existing
viewers load it unchanged.

---

## 4. cmd/crawlui — the same crawl, in a window

Same parameters as section 3. Every flag there works here and means the same
thing, because both commands build their crawler through `crawldial.Build` from
the same `Params`. They are not documented twice on purpose — two lists of the
same flags drift, and a dropped one is invisible in a map.

Look at it before pointing it at anything:

```sh
go run ./cmd/crawlui -demo
```

That plays a scripted run with no vault, no network and no devices, exercising
every state the table can show. It is the fastest way to see whether the window
is behaving, and it needs nothing set up.

Against the lab:

```sh
go run ./cmd/crawlui \
  -seed 192.168.100.2 -vault ~/.pathfinderssh/vault.json \
  -domain local.lab -allow-domain local.lab -depth 4 \
  -o lab-map.json -save-run ~/.pathfinderssh/last-run.json -v
```

Flags that exist only here:

    -demo             scripted run, no lab required
    -demo-step        pace it (default 40ms)
    -save-run         save this run for the next comparison
    -last-run         load a previous run into the comparison tab
    -strict-hostkey   require keys already in known_hosts

`-save-run` and `-last-run` are the pair worth using. Save one run, pass it as
`-last-run` on the next, and the "Since last run" tab shows what appeared,
what stopped answering, what changed platform, and which devices started
spending more credential attempts than they used to. Without a saved run that
tab is simply empty.

Note there is no insecure host-key flag. `-strict-hostkey` requires keys you
already have; the default trusts an unknown key on first contact and records
it, which is the normal case for discovery. A key that *changed* fails closed
either way, and turning that check off is deliberately a thing you can only do
from the CLI.

Reading the window:

- **Reached / Failed / Not dialed** — the third is the one to look at. Those
  devices were never connected to, and they appear in the map as leaves that
  look exactly like real edge devices. Click the counter to see them.
- **Try** — `2 !` means a credential was rejected before one worked. That is a
  real failed login against a real account.
- **Via** — which device's neighbor table produced this row. Blank for a seed.
- **Decisions** — should be short. If it is long, something systemic is off.

The Stop button cancels the crawl, and Ctrl-C does the same thing rather than
killing the process. Devices abandoned that way are reported with a reason
rather than dropped.

---

## 5. cmd/pfterm — the terminal

This is the manual smoke harness for the ported widget, not a finished app.

SSH:

```sh
./pfterm -ssh admin@192.168.100.2 -legacy
```

Telnet:

```sh
./pfterm -telnet 192.168.100.2
```

Serial — list ports first:

```sh
./pfterm -ports
./pfterm -serial /dev/ttyUSB0 -baud 9600
```

Other flags: `-key`, `-insecure` (skip host-key verification; disposable lab
gear only), `-light`, `-font`, `-log` to write a transcript.

**Leave `-rowoffset` and `-coloffset` at 0.** The flag still defaults
`-rowoffset` to 2, which is wrong — that value was compensating for a
gutter-width bug that has since been fixed, and `ui.Defaults()` is correctly
0/0. Any non-zero offset now is evidence of a measurement bug, not
configuration. Tracked in `DEFERRED.md`.

The harness docstring carries the by-hand checklist: reflow on resize,
scrollback reaching true top, alternate-screen paint under `btop` or `vi`,
wide-character columns, and a dropped link surfacing.

---

## A wrinkle worth knowing

The three host-key flags do not agree with each other:

    reach     -hostkey strict|tofu|insecure
    crawl     -insecure-hostkey
    pfterm    -insecure

Three spellings of one policy, inherited from three different starting points.
They collapse when the binaries merge into `cmd/pathfinder`; until then, check
`-h` rather than guessing.