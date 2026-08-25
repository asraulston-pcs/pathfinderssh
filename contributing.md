# Contributing to PathfinderSSH

This project exists because network gear is not uniform and no one person has
all of it on a lab bench. The most valuable thing you can send is knowledge
about a platform I do not run — what its CLI actually prints, not what its
manual says it prints. Everything below is in service of making that easy to
send and safe to merge.

## The short version

```bash
git clone https://github.com/scottpeterman/pathfinderssh
cd pathfinderssh
./test.sh          # build + gofmt + vet + tests. This is the bar.
```

If `./test.sh` is clean, open the PR. If you cannot run it, say so in the PR
description — that is useful information, not a disqualification, and it tells
me which parts of your change to check by hand.

## What is most wanted

**Platform support.** I run Cisco, Arista and Juniper. Everything else in this
codebase came from someone else's lab or was written from documentation and is
correspondingly less trustworthy. If you have hands on any of the following, a
capture of its `show version`, its LLDP/CDP detail output, and whatever it
answers when you try to turn its pager off is worth more than a patch:

- ArubaOS-CX, ArubaOS-Switch (ProVision), HP/H3C Comware 5 and 7
- ExtremeXOS, MikroTik RouterOS, Huawei VRP
- Anything with a serial console and an opinion about line endings
- Anything that does something strange during the SSH handshake — legacy
  kex/cipher requirements, banners that arrive after the prompt, hosts that
  drop the connection instead of rejecting auth

**Real output beats a guess, every time.** A pasted capture with the hostnames
scrubbed is a contribution even with no code attached.

## Adding a platform

Four extension points, in dependency order. You do not need all four — a
fingerprint entry alone is a useful PR.

**1. Fingerprint** — `internal/netexec/fingerprint.go`. One entry in the
`probes` ladder: the command that disables paging, the command that prints a
version, and one or more regexes mapping version output to a platform name.
Ordering matters and is not cosmetic: a probe whose version command is also
valid syntax on a *different* platform must sit behind a paging command that
platform does not understand, or it will hang on a pager it never disabled.
If you reorder, say why in a comment.

**2. Capture commands** — `internal/capture/spec.go`. What this platform
answers for each capture type: `RunningConfig`, `StartupConfig`, `Inventory`,
`ARPTable`, `MACTable`. A missing entry means that capture type is skipped for
that platform, which is a fine place to start.

**3. TextFSM template** — `internal/tfsm/templates/`, registered in the
`selection` map in `internal/tfsm/tfsm.go`. This is where a mistake is
expensive: a template that parses *partially* writes real-looking but wrong
edges into the topology, which is worse than parsing nothing. Two habits that
prevent it:

- Mark enough values `Required` that a near-empty record cannot be emitted.
  A port header line alone should not produce an edge.
- Put a comment block at the top of the template saying what device and
  firmware it was built from, what you confirmed live, and what you did not.
  I would much rather merge a template that says "not verified on version 7"
  than one that is silent about it.

**4. Crawl plan** — `internal/crawler/plan.go`. Which commands the crawl runs
on this platform to find neighbors. Set `BestEffort: true` on anything not
confirmed against real hardware, so a template gap degrades to "no neighbors
found" rather than aborting the device's crawl.

## Testing

`./test.sh` runs build, gofmt, vet and tests. Useful flags: `-r` for the race
detector, `-f` to fix formatting rather than report it, `-p ./internal/netexec/...`
to limit to one package, `-n 5` to hunt flakes.

**`internal/fakedev` is a real in-process SSH server** that behaves like a
device — it answers auth, grants a PTY, echoes what you type, and replies from
a command table. Use it to test the engine: prompt detection, echo stripping
across read boundaries, the probe ladder, paging. `internal/netexec/live_test.go`
has worked examples.

What fakedev cannot do is tell you the field knowledge is right. A fixture says
what we *believe* a device says. Only the device says what it says. So:

- Engine behavior → prove it with fakedev, in a test.
- Platform behavior → prove it against real gear, and write in the comment
  what you ran it against and when.

**Do not index test cases by position into a table you also modified.** If you
add a probe, every `probes[N]` in a test after it now points somewhere else,
and the failure surfaces as a wrong-looking classifier rather than "your
indices shifted." Key on something stable.

## House rules

**No production references anywhere.** Not in code, not in comments, not in
test fixtures, not in commit messages. Use lab naming throughout — `lab-sw1`,
`lab-core1`, RFC 5737 addresses. Scrub hostnames, addresses, serial numbers,
and community strings out of any capture you paste. This applies to your
employer's estate as much as mine.

**No weakened security in shipped code.** `ssh.InsecureIgnoreHostKey`, skipped
certificate verification, and hardcoded credentials do not go in the build.
A diagnostic tool that needs them belongs behind a build tag, with the reason
in a comment. Local lab convenience is legitimate — making it the default is
not.

**Header comment on every file** giving its repo-relative path and what it is
for. Look at any existing file for the pattern. The comments in this codebase
tend to explain *why* a thing is shaped the way it is, including what went
wrong before it looked like that; that convention is deliberate and I would
like it kept.

**gofmt clean.** `./test.sh -f` will fix it.

**No demo or scripted-walkthrough code** in the main tree.

## Pull requests

- Branch off `main` and rebase before opening if `main` has moved. Two of the
  first PRs to this project were based on a tag several releases back, which
  meant they silently missed APIs that had changed underneath them.
- One concern per PR where you can manage it. A platform addition and a
  terminal fix in the same branch are two reviews, not one.
- In the description, say what you validated and how: which device, which
  firmware, what you ran. And say plainly what you did *not* validate. That
  sentence is the single most useful thing in a PR to this project — it tells
  me exactly where to spend my review time.
- Expect questions about anything touching the shared path — `Normalize`, the
  prompt regex, the session file format. Not because I doubt the change, but
  because a change there affects every platform, including the ones neither of
  us can test.

## Reporting a device that does not work

Open an issue with:

1. Vendor, model, firmware version.
2. What PathfinderSSH did — the fingerprint result, the error, the truncated
   output.
3. A raw capture if you can get one. PathfinderSSH's session logging tees the
   exact byte stream, escapes and all, which is far more useful than cleaned
   text — the interesting bugs live in the escape sequences and line endings
   that cleaning removes. Scrub it before posting.

## Licence

GPL-3.0. By contributing you agree your work ships under it.
