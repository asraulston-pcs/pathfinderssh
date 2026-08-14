# Crawl

A crawl starts at one or more devices you can reach, logs in, reads what each
one knows about its neighbours, and works outward. The result is a topology map
and a list of every device it reached.

It only ever reads. There is no configuration path in this program at all.

## Before you start

Three fields decide whether a crawl produces anything, and two of them are easy
to leave alone because the run looks identical either way:

1. **Seeds** — where it starts. Without one there is nothing to crawl.
2. **Legacy KEX and ciphers**, on the same tab — if any of your gear predates
   the current SSH defaults, this is the difference between that half of the
   estate answering and none of it answering.
3. **Map output**, on the Output tab — **it has no default.** Left blank, the
   crawl runs, reaches every device, reports success, and writes no map.

The rest of the dialog is tuning. These three are the run.

## Crawl tab

![The Crawl tab](images/crawl_dialog.png)

| Field | What it does |
| --- | --- |
| Seeds | Where to start. One per line or comma separated. |
| Depth | How many hops from a seed the crawl may travel. |
| Concurrency | How many devices are dialled at once. |
| Command timeout | How long a single command may take before that device is given up on. |
| Domain suffixes | Suffixes to strip from discovered names, e.g. `lab.local`. Keeps `eng-leaf-1` and `eng-leaf-1.lab.local` from being treated as two devices. |
| Allow domains | Restricts which neighbours are dialled at all. Anything outside these domains is recorded but not visited. |
| Exclude | Substrings matched against platform, hostname and sysname. A match is skipped. |
| Host keys | TOFU or Strict — see [Sessions](#sessions-host-key-policy). TOFU is the default here. |
| Legacy KEX and ciphers | Allow older key exchanges and ciphers for this run. |

**Depth** is the control worth understanding. Depth 1 is the seed's immediate
neighbours; depth 3 typically covers a small site; a large estate wants more.
Depth is also the cheapest way to bound a first crawl of a network you do not
know yet — start shallow, look at what came back, go deeper.

**Allow domains** and **Exclude** are how you keep a crawl inside your own
estate. A neighbour table on a peering edge will happily name devices belonging
to somebody else, and neither you nor they want you dialling them.

Host keys defaults to **TOFU** here, unlike a capture. A crawl exists to meet
devices it has not met before, so requiring every key to already be known would
mean the first crawl of a new estate fails on every device.

## Credentials tab

| Field | What it does |
| --- | --- |
| Credential tags | Only vault credentials carrying all of these tags are offered. Blank offers everything. |
| Username / Password / Key file | Credentials for this run, when you are not using the vault. |
| known_hosts | Which file host keys are checked against. |

There is no vault field here on purpose. The vault is unlocked once, by the
application; a path typed into a run dialog would have to be opened again on
the run's own goroutine, with nowhere to ask for a master password.

## Output tab

| Field | What it does |
| --- | --- |
| Map output | Where `map.json` is written. **Blank writes no map.** |
| Save run | Records this run for a later comparison. Blank records nothing. |
| Compare with | A previous run file. The result shows what changed since. |
| Trust one-sided link claims between discovered devices | Keep a link only one of the two devices reported. |
| Log progress to stderr | Verbose output, for when something is going wrong. |

**Map output is blank by default, and that is the mistake worth naming.** The
run looks identical, the table fills in, everything succeeds — and there is no
map at the end, and no way to produce one except by crawling the estate again.
Fill it in before you start, every time.

The map is also what the session tree is built from, so a crawl with no map
output leaves you with nothing to import and no devices to capture from.

**Save run** and **Compare with** together answer "what changed since last
time": which devices appeared, which went away, which neighbours moved. Point
Compare with at the file a previous run saved.

**Trust one-sided link claims** decides what to do when device A reports a link
to device B but B does not report it back. That happens legitimately — a device
running only CDP next to one running only LLDP, or a neighbour whose table has
not aged out yet. Off is the conservative reading: a link both ends agree on is
a link. Turn it on when you know the estate is mixed and the map looks sparser
than the network is.

## Reading the result

![A crawl run](images/crawl.png)

The table has a row per device: the depth it was found at, the platform
detected, whether it was reached, which credential worked, how many attempts it
took, its neighbour count, and which device it was reached via.

**Decisions** underneath is the part to read when something looks wrong. It
records the judgements the crawl made — a device that identified itself under a
different name than expected, a name that would not resolve so the crawl
retried by address, a neighbour that was skipped. These are the things that
would otherwise be invisible in a run that reports success.

**Since last run** shows the comparison, when **Compare with** was set.

## After the crawl

The map file is the durable output. **File → Import topology map** turns it
into sessions; the **Map** button opens it in a browser. Both read the file, so
neither needs the crawl to still be running — the map is viewable any time
after.
