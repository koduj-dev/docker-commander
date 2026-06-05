# Stacks

[← Manual index](README.md)

A **stack** is a group of containers that share a Compose project — Docker
Commander discovers them from the standard `com.docker.compose.project` label,
so **stacks started with the `docker compose` CLI on the host show up here too**,
not just ones deployed from a [Project](projects.md).

Each stack card shows a coloured **status LED** — 🟢 all running, 🟡 partially
running or an unhealthy container, 🔴 stopped — its services, and (for
DC-managed projects) a link back to the [Project](projects.md).

## Browsing
- **Filter** by name / service / image, and by state (🟢 running / 🟡 issues /
  🔴 stopped). The filter is remembered per user.
- **Collapse / expand** a stack (or all at once) to keep a long list scannable.
- **Hover** a service to see a floating card with its state, image, full status
  and published ports. Click a container name to open its
  [detail page](containers.md).

## Actions (whole stack)
- **Start / Stop / Restart** — applied to every container in the stack.
- **Remove** — force-removes the stack's containers and its Compose networks;
  named volumes are kept (like `docker compose down`).
- **View compose file** — reads the stack's `compose.yml` from the host (the
  path comes from the `com.docker.compose.project.config_files` label): directly
  for the local daemon, over **SSH** for ssh hosts. Plain-TCP hosts can't reach
  the host filesystem. **Copy** or **download** it from the viewer.

## Tips
- A stack you created from a [Project](projects.md) shows a folder icon linking
  straight to its editor; conversely, a Project links to **Open in Stacks**
  (which filters to and expands that stack).
- Managing one stack from both DC and the host `docker compose` CLI can drift —
  prefer managing a given stack from one place.
