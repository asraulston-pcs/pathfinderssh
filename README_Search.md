# Search

Search greps every capture in a store. It answers "where is this configured"
across an entire estate, from files already on your disk, in milliseconds — no
device is contacted.

![The search dialog](images/search_dialog.png)

| Field | What it does |
| --- | --- |
| Find | The text to look for. Matching is literal — not a regular expression. |
| Store | The capture store to search. |
| Types | Which capture types to include. |
| Case sensitive | Off by default. |

Search reads the **current version** of every capture, not the history. That is
a deliberate limit: a year of nightly captures of an unchanged device is one
file recorded three hundred times, and searching all of them would return the
same line three hundred times.

## Reading the results

![Search results](images/search_result.png)

Each hit is the device, the capture type, the line number and the matching
line. Selecting one loads the whole file underneath with every occurrence
highlighted, so you can read the match in context — the rest of the route-map,
the rest of the interface.

**Open session** connects to the device the hit came from. That is the point of
the whole feature: find it, then be on it.

Results are capped. A one-character search across a large store would otherwise
build a table with hundreds of thousands of rows; when the cap is reached the
result says so, and what you have is the beginning of the answer rather than an
arbitrary sample.

## MAC and ARP captures

`mac-table` and `arp-table` captures are searched the same way as a
configuration, which turns "which port is this MAC on" into a local question
answered from disk instead of a walk of the estate. Capture them on a schedule
and the store also answers it for last Tuesday.

One caveat, and it is a real one: **matching is literal**, so an address has to
be typed the way the device prints it. `0011.2233.4455` will not match
`00:11:22:33:44:55`. Search in the format the platform uses, or search a
fragment that survives the difference — the middle of an address is written the
same way on both once the separators are out of it.

## What it is good for

- Which devices reference a prefix, a VLAN, an ASN, a neighbour address.
- Which switch port a MAC address is on, and which IP resolved to it.
- Which devices still carry a decommissioned server, an old NTP source, a
  retired ACL entry.
- Confirming a change landed everywhere it was supposed to.
- Confirming a setting is absent everywhere it is supposed to be absent.

The last one is worth its own mention. Proving something is *not* configured
anywhere is normally the expensive question, and against a local store it costs
the same as any other search.