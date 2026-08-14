# PathfinderSSH

A single-binary network terminal that maps the network it connects to.
Crawl → map → click → session, with one engine underneath:
**dial → fingerprint → run → { parse | store }**.

Private and closed source. This README is for whoever is working in the tree.
For *why* the product exists, read the top of [README.md](../README.md). For
*when*, read [ROADMAP.md](../ROADMAP.md).

---

## Binaries

| Command | What it is |
|---|---|
| `cmd/crawl` | SSH-only topology crawler. BFS from one or more seeds, fingerprint each device, parse CDP/LLDP neighbors, emit topology-map JSON. |
| `cmd/reach` | Runs one or more commands against a device over a PTY shell with prompt detection. Each `-c` is one command, sent verbatim. |
| `cmd/pfvault` | Creates and manages the credential vault `crawl -vault` reads. Never takes a secret on argv. |
| `cmd/crawlui` | The crawler in a window: live table, three outcome counters, decisions, run-to-run comparison. Same parameters as `cmd/crawl`; `-demo` plays a scripted run with no lab. |
| `cmd/pfterm` | Manual smoke harness for the terminal widget. Not a product binary and not a test — it exists so a human can drive a real session. |

These collapse into a single `cmd/pathfinder` before v1. `pfterm` stays
separate; a harness is not a deliverable.

```sh
go build ./...
go build -o bin/crawl ./cmd/crawl

# Drive a terminal against lab gear
go run ./cmd/pfterm -ssh admin@lab-sw1.lab.example
go run ./cmd/pfterm -telnet lab-console1.lab.example:2001
go run ./cmd/pfterm -serial /dev/tty.usbserial-A900 -baud 9600
```

Building anything that imports `internal/ui` needs a C toolchain: Fyne uses
cgo for the graphics driver. The non-GUI binaries are pure Go and
cross-compile with nothing but `GOOS`/`GOARCH`.

---

## Layout

One module. Everything is under `internal/`, which is not a style choice: it
makes it impossible for any of this to be imported by a public module, so the
closed side cannot leak by accident.

    internal/
      sshcore     the SSH connection: dial, auth ladder, host keys,
                  algorithm policy, jump host
      term        interactive SSH session; defines the Transport interface
      serialx     Transport over a serial line
      telnetx     Transport over plaintext TCP, with the IAC state machine
      netexec     command execution: prompt detection, paging, output cleaning
      normalize   interface names, platform identity, prompt parsing
      tfsm        embedded pre-vetted TextFSM templates and their selection
      crawler     depth-batched BFS: dial, fingerprint, parse neighbors, enqueue
      topo        device/neighbor model and topology-map generation
      jump        bastion path resolution from YAML route-maps
      vault       secrets at rest: Argon2id + AES-256-GCM
      credres     which credential for this device, and in what order
      vaultcli    where the master password comes from
      gopyte      the VT/ANSI emulator (a Go port of pyte, forked and extended)
      ui          the terminal widget: rendering, selection, themes, sessions

The dependency direction is the point. `vault` knows about secrets and nothing
about hosts. `credres` knows about hosts and treats secrets as opaque. Neither
knows anything about SSH. `term`, `serialx` and `telnetx` all satisfy one
interface, so `ui` drives a console cable, a telnet console server and an SSH
session with no branching.

---

## Architecture

Seven documents, each covering one seam. They explain decisions rather than
restating the code, including the ones that were wrong first:

- [SSH](../README_SSH_Arch.md) — one connection layer, two session layers. Why
  automation and interactive sessions want opposite things, the single
  algorithm policy and the false host-key MISMATCH that motivated it, and why
  `Transport` reports liveness structurally instead of by matching error text.
- [Credentials](../README_Arch_Creds.md) — the vault, the resolver, and why a
  crawl needs restraint a terminal does not: a stale entry on a fabric is one
  lockout, not one shrug.
- [Mapping](../README_Arch_Maps.md) — discovery, the topology model, and the
  artifacts.
- [Runs](../README_Arch_Runs.md) — a crawl as state rather than as output: the
  event stream, the run model behind the table, parameters, and run-to-run
  comparison. Toolkit-free on purpose.
- [Fyne UI](../README_Fyne_UI.md) — desktop conventions and the lifecycle and
  threading rules that produce panics naming the wrong thing when broken.
- [Capture](../README_Capture_Arch.md) — read-and-store only, and why the store
  is content-addressed: an unchanged estate must be able to store nothing and
  say so.
- [Shell](../README_Shell_Arch.md) — how the applets are hosted in one window,
  and the four silent failure modes in moving a live widget between windows.

---

## Testing

```sh
./test.sh              # build, gofmt, vet, test
./test.sh -f           # fix formatting instead of reporting it
./test.sh -r           # race detector
./test.sh -c           # coverage totals
./test.sh -p ./internal/jump/...
./test.sh -F           # also check against scripts/floor.deps
```

`-F` rebuilds and retests against the oldest supported dependency set in a
throwaway copy, so the working tree keeps the versions it ships. It exists
because the shipping set requires a newer Go than some environments have, and
"it compiles here" is not the same claim as "it compiles."

`FLOOR_EXCLUDES` in `test.sh` holds directories left out of `-F` only —
currently `internal/ui` and `cmd/pfterm`. They build, vet and test normally;
they are skipped there because pinning Fyne's forty-odd transitive modules
would test a combination the shipping tree never uses. The GUI is validated by
building and running it.

`test.sh` targets bash 3.2, which is what macOS ships. No `mapfile`, no
namerefs, no bare expansion of a possibly-empty array under `set -u`.

---

## Security stance

Most of this product ships early and iterates. Four things do not, because a
network engineer's forgiveness has a cliff exactly where a tool touches
credentials and production gear, and no price point buys grace past it:

1. **Credential storage.** No plaintext path, ever.
2. **Host keys fail closed.** A mismatch against a pinned host is never
   overridable by convenience. `HostKeyInsecure` exists for disposable lab
   equipment and is opt-in per connection — never a fallback from a failure.
3. **The crawler is provably read-only.** Every probe command is exec-level and
   side-effect-free, as a hard rule of the codebase.
4. **Never hang a device.**

Telnet is plaintext including passwords. It is here because terminal servers
and reverse-telnet console ports have no alternative, and it is never reached
by falling back from a failed SSH connection — that would silently downgrade a
session the operator believed was encrypted.

---

## Conventions

- **Lab naming everywhere.** Code, tests, documentation, commit messages,
  screenshots. `lab-r1`, `site1.lab.example`, `10.0.0.0/8` fixtures. Crawl
  output from real networks is read for diagnosis and never committed, quoted,
  or posted. The committed map is the lab fixture and nothing else.
- **`go mod tidy` is the authority** for `go.mod` and `go.sum`. Do not
  hand-edit either.
- **A file's repo-relative path is the first line of its doc comment.**
- Comments explain why, not what. A comment that survives is usually one that
  records a failure — the pty gutter, the split UTF-8 rune, the diverging
  algorithm lists. Those are the expensive lines in the file.