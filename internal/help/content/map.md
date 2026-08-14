# The map

The **Map** button opens the topology from a crawl in your browser. It reads a
`map.json` file, so it works any time after a crawl — the run does not have to
still be open, and a map from last month opens the same way as one from five
minutes ago.

The browser is only a renderer here. The page is served by the application
itself, to your own machine, and nothing about your network leaves it.

## Getting around

**Force-directed** arranges the graph automatically. Drag nodes to fix them
where they make sense to you, then **Save** the layout — it is remembered for
that map, so the picture you arranged is the picture you get back.

**Hide undiscovered** and **Hide leaf nodes** thin out a dense map. A leaf is a
device a neighbour mentioned but the crawl never logged into — real, but known
only by hearsay.

Clicking a node shows what was learned about it: its address, its detected
platform, whether it was discovered or only mentioned. **Connect** opens a
session on that device, in the application, from the map.

![The map with a node selected](images/map_view.png)

## Export

| Export | What you get |
| --- | --- |
| PNG | An image of the map as arranged. |
| JSON | The map data. |
| Draw.io | An editable diagram. |

The Draw.io export exports **what is visible, arranged as it is now** — so hide
what you do not want, arrange it the way you want it read, and then export.
Nodes carry their name, address and platform, and links carry the interfaces on
each end.

![The Draw.io export opened in draw.io](images/map_drawio.png)

This is the path from "the network as discovered" to "a diagram somebody can be
handed", without redrawing anything by hand.

## Importing into the session tree

**File → Import topology map** turns a map into sessions.

![Importing a map into the session tree](images/import_map.png)

**Folder** names the tree folder the devices land in. **Include leaves** brings
in the devices that were only mentioned by a neighbour — they will need an
address and credentials confirmed before they connect, so leave it off unless
you specifically want placeholders to fill in.

Importing the same map again merges. Devices you have edited keep their
settings; devices that are new are added.
