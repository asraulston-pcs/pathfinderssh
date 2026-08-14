# SSH Architecture

How PathfinderSSH connects to a device, and how one connection serves both an
automation run and an interactive terminal.

Four packages and two consumers:

    internal/sshcore    the connection: dial, auth, host keys, algorithms
    internal/netexec    a session that runs commands and reads the answer
    internal/term       a session that carries bytes and nothing else
    internal/serialx    the same byte carrier, over a serial line
    internal/telnetx    the same byte carrier, over plaintext TCP
    cmd/reach           CLI consumer of sshcore + netexec
    cmd/pfterm          GUI consumer of sshcore + term

---

## The problem this solves

Two things want an SSH connection, and they want opposite behavior from it.

A crawl wants a session it can *interpret*. It sends a command, waits for the
prompt to come back, strips the echo, and hands the middle to a parser. It needs
the device to stop paginating, to stop wrapping output mid-field, and to be as
boring as possible. Colour is noise. A wide terminal is a feature. The session
is a function call that happens to take three seconds.

A terminal wants a session it must *not* interpret. Whatever the device emits
goes to the screen unread — escape sequences, cursor addressing, 256-colour SGR,
alternate-screen switches. The window size is whatever the operator dragged it
to. There is no prompt to wait for, because the operator is the one deciding
when something is finished.

Those are irreconcilable at the session layer and identical below it. Both need
the same credentials, the same host-key decisions, the same algorithm
negotiation for the same aging equipment, and the same bastion path. Building
them as two SSH clients means maintaining that agreement twice, and the failure
mode is not that they drift visibly — it is that they drift in the host-key
preference order and start reporting each other's devices as compromised.

So: one connection layer, two session layers, and a hard rule that the session
layers never dial.

---

## Layers

    ┌──────────────────────────────────────────────────────────┐
    │ cmd/reach              -c "show version"                 │
    │ cmd/crawl              discovery                         │
    │ cmd/pfterm             interactive window                │
    └───────┬──────────────────────────────┬───────────────────┘
            │                              │
    ┌───────▼──────────────────┐  ┌────────▼───────────────────┐
    │ internal/netexec         │  │ internal/term              │
    │ Run / Enable /           │  │ Transport: Read Write      │
    │ Fingerprint              │  │ Resize Close Done Err      │
    │ prompt regex, paging     │  │ no interpretation at all   │
    └───────┬──────────────────┘  └────────┬───────────────────┘
            │                              │
            │        *sshcore.Client       │
    ┌───────▼──────────────────────────────▼───────────────────┐
    │ internal/sshcore                                         │
    │ Dial: auth ladder, host keys, algorithms, jump host      │
    └──────────────────────────────────────────────────────────┘

The direction matters. `sshcore` knows how to establish a connection and nothing
about what will be done with it — it has no notion of prompts, commands,
platforms, or windows. `netexec` and `term` both take an *already established*
`*sshcore.Client` and open one channel on it. Neither can dial, which is what
makes "there is only one place credentials become a connection" a structural
fact rather than a convention.

`internal/serialx` and `internal/telnetx` sit beside `term` rather than under
it: same `Transport` interface, no SSH underneath. That is why the terminal
widget drives a console cable, a telnet console server, and an SSH session with
no branching.

---

## internal/sshcore — the connection

`Dial(Config) (*Client, error)` is the whole public surface, plus `Client.SSH()`
which exposes the underlying `*ssh.Client` for the session layers.

```go
cfg := sshcore.Config{
    Host:             "lab-r1.lab.example",
    Port:             22,
    Username:         "admin",
    UseAgent:         true,
    HostKeys:         sshcore.HostKeyTOFU,
    HostKeyPrompt:    confirmUnknownHost,
    AuthPrompt:       askForSecret,
    LegacyAlgorithms: true,
}
client, err := sshcore.Dial(cfg)
```

`Client` retains the bastion connection alongside the target connection when one
was used, so `Close()` tears down both and a caller never has to track the pair.

### The auth ladder

`buildAuthMethods` assembles, in order:

1. **Agent**, when `UseAgent` and `SSH_AUTH_SOCK` points at a live agent.
   Signers are fetched lazily at handshake time, not at config time.
2. **Public key**, from `PrivateKey` (in memory, wins) or `PrivateKeyPath`.
   An encrypted key prompts through `AuthPrompt` for its passphrase.
3. **Password**, when one is set.
4. **Keyboard-interactive**, always.

Keyboard-interactive is unconditional because it is how MFA and RADIUS arrive,
and because a surprising number of devices expose what looks like password auth
only through it. That does mean the `len(methods) == 0` guard below it is
unreachable — the list is never empty. It is left in place because it costs
nothing and the error string it would produce is one `credres` classifies
correctly anyway.

The ladder here is *within one connection attempt*: these are the methods
offered to the server, and the server picks. The other ladder — trying a
different credential after a rejection — lives in `internal/credres` and is
described in the credentials architecture. They are different mechanisms and
conflating them leads to accounts getting locked out.

### One algorithm policy, both hops

Every dial in the package goes through `algorithmPolicy()` and `hostKeyAlgos()`.
Never an inline list, and specifically never a different list for the jump hop.

This is the single most important invariant in the package, and it exists
because violating it produced a real and thoroughly confusing failure. When the
two dial paths carried different `HostKeyAlgorithms` preference orders, the same
server would offer an ed25519 key when reached directly and an ecdsa key when
reached as a bastion. `known_hosts` has one entry for that host. The second path
read the difference as a host-key **MISMATCH** — the signature of a
man-in-the-middle — for a device whose keys had never changed.

The modern set is the default. `LegacyAlgorithms` appends a tail of old KEX
groups, CBC ciphers, sha1/md5 MACs, and `ssh-rsa`/`ssh-dss` host keys. Appends,
not replaces: a legacy-enabled dial still prefers modern algorithms and only
falls back when the device offers nothing better. Turning it on weakens what is
*possible*, not what is *chosen*.

### Host keys

    known host, key matches   -> accept
    known host, key MISMATCH  -> reject, always
    unknown host, Strict      -> reject
    unknown host, TOFU        -> ask HostKeyPrompt; accepted keys are persisted
    Insecure                  -> skip verification entirely

TOFU never applies to a mismatch. A first-contact decision and a changed-key
decision are not the same decision, and giving the operator a prompt for the
second one trains them to click through the only case that actually matters.

`HostKeyInsecure` exists and is not deprecated. Disposable lab equipment gets
rebuilt weekly and there is no value in pinning its keys; forcing verification
there teaches people to answer yes reflexively, which is worse than an explicit
opt-in flag. It is opt-in per connection and never a fallback.

One deliberate difference from the TetherSSH baseline: with no `known_hosts`
file, the baseline silently fell back to `InsecureIgnoreHostKey`. Here the file
is created empty and verification proceeds normally. An unverifiable host is a
decision for the policy to make; it is never a silent downgrade.

---

## internal/netexec — the session that reads

`Open(client, Options) (*Session, error)`, then `Run(cmd) (string, error)`.

The PTY is requested as **`vt100` at 60 rows by 511 columns**. Both numbers are
deliberate. `vt100` because the goal is output a parser can read, and a device
that thinks it is talking to something dumb will not decorate. 511 columns
because network operating systems wrap output at the terminal width, and a
wrapped line splits a field in half and destroys the parse — a width no device
will reach means nothing wraps.

After the first prompt, `PagingDisable` is sent once (`terminal length 0` and
its equivalents). Then each `Run` writes the command and blocks until
`PromptRegex` matches at the end of the accumulated output, bounded by
`CommandTimeout`. `StripEchoAndPrompt` removes the echoed command from the front
and the trailing prompt from the back, leaving the output.

`ConnectTimeout` is separate from `CommandTimeout` and defaults to it, because
the wait for the *first* prompt is a different animal: banners, MOTDs, and slow
control planes can make the initial wait much longer than any subsequent
command.

`Enable()` handles the privilege escalation prompt. `Fingerprint()` runs a small
sequence of version probes and classifies the result into a `Platform`.

`Close()` tears down the shell channel and leaves the `sshcore.Client` open.

---

## internal/term — the session that doesn't

```go
type Transport interface {
    io.ReadWriteCloser
    Resize(Size) error
    Done() <-chan struct{}
    Err() error
}
```

Read, write, close — plus the two things a byte stream cannot express on its
own: how big the window is, and whether the far end has gone away.

`Done()` and `Err()` are the interesting part, and they are the reason this
interface looks different from a plain `io.ReadWriteCloser`. A read error alone
cannot tell you what happened. A serial unplug and a deliberate `Close()` return
the *identical* error text from the underlying library. A remote shell exiting
cleanly and a connection being reset both surface as read failures. The
TetherSSH backend answered this by matching on error strings — `"timeout"`,
`"closed"`, `"use of closed network connection"` — with a side flag recording
whether a teardown had been intentional.

Here the transport answers structurally. `Err()` is non-nil only when the far
end went away on its own. A local `Close()` reports nil, and both
implementations guarantee it by recording the terminating error *before* tearing
down the thing whose teardown would produce a spurious one. A clean remote exit
also reports nil, because `io.EOF` is folded away. So the consumer's rule is a
single `if err != nil`, and roughly forty lines of string matching do not exist.

`Open(client, Options)` requests a PTY as **`xterm-256color`** — the opposite
choice from netexec, for the same reason netexec chose `vt100`. The theme layer
above the terminal is built on 256-colour SGR, and a device told it is talking
to a vt100 is entitled to withhold it. Devices that do not recognise the value
fall back to dumb behaviour rather than failing the PTY request.

Two ordering constraints in `Open` that are easy to get wrong and produce
delayed, confusing failures:

- **Env before PTY.** A server that accepts environment variables at all wants
  them before the channel becomes a terminal. Rejections are collected into
  `EnvErrors` rather than treated as fatal, because network operating systems
  refuse env as a matter of course and that is not a reason to fail the session.
- **Pipes before `Shell()`.** `x/crypto` wires stdin/stdout/stderr at start and
  rejects the calls afterwards.

And one that is easy to get backwards: `RequestPty` takes **rows first, then
columns**. Reversed, the session works fine right up until the first line wraps.

Stderr is drained unconditionally, into a bounded 8 KiB buffer. This is not
diagnostics-gathering, it is a requirement: an SSH channel has a per-stream
flow-control window, and a stream nobody reads will fill its window and then
stall *the entire channel*, stdout included. The bounded retention is a bonus —
when a session dies during startup, the reason often arrives on stderr and
nowhere else.

`OwnsClient` decides whether closing the session also closes the connection
under it. Set it when the client was dialed for this session alone, which is the
normal case for a terminal window. Leave it false when the client is shared.

---

## internal/telnetx — the plaintext transport

Same `Transport` interface, no SSH. `New(Config)` then `Connect()`; the TCP
dial is the entire handshake, and the device's login prompt, if it has one,
arrives as ordinary data.

Telnet is not SSH-shaped underneath. It is raw TCP plus an in-band
option-negotiation protocol (RFC 854), where every control sequence is
introduced by the IAC byte, 0xFF. So `Read` is a state machine: it consumes
those sequences, answers them on the socket, and hands upward only the
application data. A `DO SUPPRESS GO-AHEAD` never reaches the emulator as
garbage. The parse state is carried across `Read` calls, because an IAC
sequence can be split across TCP segments.

Three behaviours worth naming, each of which is a bug if you skip it:

- **Reply only on change.** The classic telnet failure is two peers
  ping-ponging WILL/DO forever. Declared option state is tracked per option and
  a request that would not change it gets no reply.
- **CR LF and IAC doubling on write.** RFC 854 makes CR LF the telnet newline
  while the widget emits a bare CR on Enter, so a lone CR is expanded. Any
  literal 0xFF in user data is doubled, per RFC 854 section 3 — otherwise
  typing that byte begins a command.
- **NAWS.** Unlike serial, `Resize` here is real: the size is pushed by
  subnegotiation so a device that honours NAWS lays out at the true width
  instead of a hardcoded 80 columns. Invalid sizes are dropped rather than
  sent, since a 0x0 frame tells the device the window has no width.

It honours the same `Err()` contract as the other two: a local `Close()` reports
nil, a dropped connection reports the failure.

Telnet is plaintext, including whatever password the device prompts for. It is
here because the equipment that needs it has no alternative — terminal and
console servers, reverse-telnet console ports on GNS3 and dynamips, and legacy
gear with no SSH stack. It is never reached by falling back from a failed SSH
connection, which would silently downgrade a session the operator believed was
encrypted.

---

## internal/serialx — the other transport

Same `Transport` interface, no SSH. `New(Config)` then `Connect()`; opening the
port is the entire handshake, since there is no auth, no host key, and no
keepalive.

`Resize` is a documented no-op that returns nil rather than an error, so callers
do not have to branch on which transport they are holding. A serial console has
no window-change concept; the terminal still measures and lays out its grid
locally, nothing is signalled downstream.

It honours the same `Err()` contract as the SSH session: a local `Close()`
reports nil, an unplugged adapter reports the failure. That contract is what
lets the widget above treat the two identically.

---

## Who owns the connection

The crawler takes a `DialFunc`:

```go
type DialFunc func(t DialTarget) (*sshcore.Client, error)
```

It never constructs an `sshcore.Config`. All credential, jump, and host-key
policy stays in the CLI layer, and the crawler learns nothing about how any
connection was made. `cmd/crawl/dialer.go` is the one place a credential becomes
an `sshcore.Config` — see the credentials architecture for the resolution order
that feeds it.

The payoff of separating connection from session shows up in the product's core
loop. A crawl authenticates to a device and opens a `netexec` session on it. The
operator later clicks that device on the map and wants a terminal. That is a
second session on a client that is already authenticated — `term.Open` on the
same `*sshcore.Client`, no second credential decision, no second host-key
prompt, no second handshake.

---

## What is not built

- **Multi-hop jump paths.** `sshcore` dials through exactly one bastion.
  `internal/jump` can resolve a multi-hop path and bind credentials to
  individual hops, but nothing consumes that yet. Chaining the dial is the one
  piece of genuinely new connection code the roadmap still needs.
- **Windows SSH agent.** `agentAuth()` returns nil on Windows; named-pipe agent
  support is deferred. Key files and passwords work there, agent forwarding does
  not.
- **Connection pooling.** `OwnsClient` makes shared clients *possible* but
  nothing shares one yet. The crawl opens and closes a client per device, and
  the terminal dials its own. Reusing a crawl's client for a terminal session is
  the obvious next step and is not wired.
- **Keepalive.** TCP keepalive is set at 30s on the dialed connection, which
  survives host sleep better than application-level pings. There is no
  application-level keepalive, and no per-session idle timeout beyond whatever
  the device enforces. The terminal's anti-idle keystroke (see the terminal
  layer) is a different mechanism and lives above this one.