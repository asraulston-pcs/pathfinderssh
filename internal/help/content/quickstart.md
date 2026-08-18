# Quickstart

PathfinderSSH is a terminal, a discovery crawler, a configuration store and a
map, in one program on your laptop. There is no server to stand up, no agent to
install on a device, and nothing is written to your network — every command it
sends is a read.

This walkthrough takes one device you can already reach and turns it into a
mapped network with a configuration baseline you can search. It takes about ten
minutes.

## The order matters

Each step proves something the next one assumes:

1. **Add a session for one device** — a seed you know is reachable.
2. **Connect to it.** This proves the address, the credentials and the
   transport before anything larger depends on them.
3. **Put those credentials in the vault**, so the crawl and the capture can use
   them without being handed a password each time.
4. **Crawl from that seed.** The crawler logs in, reads neighbours, and walks
   outward.
5. **Import the map into the session tree.** Every device found becomes a
   session you can open.
6. **Capture** running-config and inventory across those sessions.
7. **Search** the captures, and open the map whenever you want it.

Most first-run problems are step 2 wearing a disguise: a crawl that reaches
nothing is usually a credential or a transport question, not a crawl question.
Doing step 2 on its own is what keeps those separate.

## 1. Add a session for the seed

Use the **+** button under the session tree. The only fields that matter now
are the name, the host and the transport.

Pick a seed with a wide view of the network. A core or distribution device
knows about far more neighbours than an access switch does, so the crawl
reaches more of the estate from fewer hops.

## 2. Connect

Open the session. If you land on a prompt, everything downstream will work.

If you do not:

- **Authentication failed** — the credentials are wrong, or the device wants
  keyboard-interactive and you supplied a key. Try the username and password
  directly in the session first.
- **Connection refused or timed out** — the address or the port, not the
  credentials.
- **No matching key exchange method / no matching cipher** — the device is
  older than the defaults. Tick **Allow legacy KEX/cipher/MAC** on the
  session's Advanced tab. See [Sessions](#sessions).

## 3. Add the credentials to the vault

**Vault → Manage credentials**. Add the username and password that just
worked, and make it the default so runs that name no credential still have one.

The vault is a single encrypted file. A crawl or a capture asks it for a
credential per device, so you set this up once instead of typing it per run.
See [Credentials and the vault](#vault).

## 4. Crawl from the seed

**Crawl** on the toolbar. Three fields matter:

- **Seeds** — the address you just connected to.
- **Legacy KEX and ciphers** — tick it if that session needed it.
- **Map output**, on the **Output** tab — a path you will remember.

Depth is how many hops from the seed the crawler may travel; the default of 3
covers a small estate comfortably. Everything else can be left alone for a
first run.

![The Crawl tab of the crawl dialog](images/crawl_dialog.png)

**Map output has no default, and blank means no map is written.** The run looks
exactly the same either way: devices are reached, the table fills in, the run
reports success — and there is nothing at the end. The map is what the next two
steps are built from, so a crawl without it has to be run again from scratch.
Fill it in before you press Start.

![The Output tab, where Map output lives](images/crawl_output.png)

The run table fills in as devices are reached. The **Decisions** pane under it
explains anything surprising, including devices that were unreachable by name
and retried by address.

![A crawl in progress](images/crawl.png)

## 5. Import the map into the session tree

**File → Import topology map**, and pick the `map.json` the crawl wrote. Give
the folder a name — the estate, the site, the customer.

![Importing a topology map](images/import_map2.png)

Every discovered device becomes a session, with its address and platform
already filled in. Importing again later merges: devices that are still there
keep their settings, new ones are added.

**Include leaves** brings in devices a neighbour mentioned but the crawl never
logged into. They are real devices, but nothing has confirmed how to reach
them, so leave it off for a first import.

## 6. Capture configurations

**Capture** on the toolbar. Now that the tree is populated, point the capture
at the sessions rather than typing a device list:

- **Session file** — your `sessions.yaml`.
- **Match sessions** — `*` for everything, or a glob like `eng-*`.
- **Types** — tick `running-config` and `inventory`. `arp-table` and
  `mac-table` are there too, and cost nothing extra to add; they keep a rolling
  five versions rather than a full history, because they change on their own.
- **Store** — a directory. It is created if it does not exist.

Each device is dialled once and every selected type is read over that one
session. Nothing is written to any device.

![A finished capture](images/capture_results.png)

Run it again tomorrow and anything unchanged is recorded as unchanged rather
than stored again, so the store becomes a history of what actually changed.

## 7. The map, and finding things

The map opens in your browser from the **Map** button, any time after a crawl —
it reads the `map.json` file, so it does not need the crawl to still be
running. Click a node and you can connect straight to that device.

![The map, with a node selected](images/map_view.png)

**Search** greps every captured configuration in the store. Searching for an
address, a VLAN, a neighbour or a route-map answers "where is this configured"
across the whole estate in a few milliseconds, and a hit opens a session on the
device that has it.

## Where things live

| What | Where |
| --- | --- |
| Sessions | `~/.pathfinderssh/sessions.yaml` |
| Vault | `~/.pathfinderssh/vault.json` |
| Settings | `~/.pathfinderssh/settings.json` |
| Transcripts | `~/.pathfinderssh/logs` |
| Captures | wherever you set **Store** |
| Maps | wherever you set **Map output** |

**Settings → Paths** shows the paths this build actually resolved, with a Copy
button. That page answers "which file is it really using", which is a different
question from "which file should it be using".