# Networks & Topology

[← Manual index](README.md)

## Networks
A card per network with driver, scope, subnets, an **internal / external** flag
and the attached-container count. Search and filter (**in use / unused /
internal / all**) as elsewhere.

- **Create** (header) — a user-defined network: name, **driver** (default
  `bridge`), optional **subnet** / **gateway**, and the **internal** (no external
  connectivity) and **attachable** (containers outside a compose stack can join)
  flags.
- **Prune unused** (header) — remove every network not used by any container.

### Network detail
Click a card to open its detail modal, which shows the network's attached
containers as a **list** or a **graph** (toggle, top-right):

- **List** (default) — a compact table: state, image, stack, **published ports**
  and the container's **IP** on this network, with a per-row **disconnect**.
- **Graph** — the network and its containers as an interactive force-directed
  diagram (the same renderer as the Topology page).
- **Connect** — attach any container not already on the network.
- **Inspect** (raw JSON) and **Remove**. Predefined networks (`bridge`, `host`,
  `none`) can't be removed; the daemon also refuses a network that still has
  containers attached — disconnect them first.

## Topology
An interactive graph of **containers ↔ networks** (React Flow), laid out with a
force-directed simulation so containers cluster around their networks and the
whole graph spreads across the width (rather than one tall column).

- **Pan / zoom**, drag nodes to rearrange (edges re-route cleanly), and use the
  controls bottom-left, the minimap, or the **fullscreen** button top-right.
- Click a container node to jump to its [detail page](containers.md).
- **Find container** search (name / image / stack) narrows the graph to the
  matches and the networks they're on; a **stack** dropdown filters to a single
  compose project. A badge shows the current node count.
- Toggles: **Hide empty networks** and **Show stopped** — both default to a clean
  view (running containers, non-empty networks); the filters persist across
  reloads.
- **List view** (toggle, top-right) — a dense, filterable table of containers
  (state, image, stack, ports, networks) as a legible fallback at scale.

## Tips
- On a busy host, use the search / stack filter or the list view; empty networks
  are hidden automatically while a filter is active.
