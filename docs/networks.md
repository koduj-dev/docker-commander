# Networks & Topology

[← Manual index](README.md)

## Networks
A card per network with driver, scope, subnets, an **internal / external** flag
and the attached-container count. Click a card to open its modal, which draws
the network in the centre with its connected containers around it.

From the modal you can **Inspect** (raw JSON) and **remove** the network.
Predefined networks (`bridge`, `host`, `none`) can't be removed; the daemon also
refuses a network that still has containers attached — disconnect them first.

## Topology
An interactive graph of **containers ↔ networks** (React Flow):

- **Pan / zoom**, drag nodes to rearrange (edges re-route cleanly), and use the
  controls bottom-left or the **fullscreen** button top-right.
- Click a container node to jump to its [detail page](containers.md).
- Filters (top-right): **Hide empty networks** and **Show stopped** containers.
- Stopped/exited containers are included by default (built from each
  container's network settings, so they don't vanish when not running).

## Tips
- On a busy host the graph is large; it opens at a readable zoom — use the
  minimap and fullscreen to navigate, or filter out empty networks.
