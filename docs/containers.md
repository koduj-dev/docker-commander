# Containers

[← Manual index](README.md)

![Container detail](images/container_detail.png)

## The list
Search by name / image / id / state, choose a page size (10–100), and act on a
row: **start**, **stop**, **restart**, **pause/unpause**, **kill**. Click a name
to open the detail page.

### Create / run
**Create container** opens a form covering the common `docker run` options:

- **Image** (required) and optional **name** and **command**.
- **Ports** — one `host:container[/proto]` per line (e.g. `8080:80`, `53:53/udp`).
- **Env** — `KEY=VALUE` per line. **Volumes** — `src:dst[:ro]` per line.
- **Restart policy**, **memory limit (MB)**, **CPUs**, and *start immediately*.

## Detail page
Live **CPU** and **memory** charts plus a **history** chart (15m / 1h / 6h).
Header actions: **Commit** (snapshot to a new image), **Settings** (rename +
update limits/restart policy at runtime), **Export** (download the filesystem as
a tar), **Inspect** (raw JSON), and lifecycle buttons.

Tabs:

- **Overview** — status, health, command, networks, ports, mounts.
- **Logs** — live `stdout`/`stderr` tail.
- **Console** — an interactive shell (xterm.js) into the running container.
- **Processes** — `docker top`, refreshed periodically.
- **Files** — a file browser: navigate directories, **download** a file or a
  whole directory (as a tar), **upload** into the current directory, and delete
  paths (this is `docker cp` under the hood; needs a shell/`ls` in the image).
- **Changes** — filesystem changes since start (`docker diff`: added / modified
  / deleted).
- **Env** — environment variables.

## Tips
- **Commit** is handy to capture a debugged container as an image you can then
  [push](registries.md) or [save](images.md).
- A **read-only** user can view everything here but the action buttons (start,
  exec, upload, delete…) are blocked. See [Users & roles](users.md).
