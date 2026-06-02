# Dashboard

[← Manual index](README.md)

![Dashboard](images/dashboard.png)

The landing view summarises the **selected host**.

## Host facts
Top row of cards: hostname + Docker version, CPU count + architecture, total
memory + OS, and counts of **running** / **stopped** containers and **images**.

## Disk usage
A breakdown from `docker system df`:

- **Layers total** — combined size of image layers on disk.
- **Images / Containers (rw) / Volumes / Build cache** — count and reclaimable
  size per category.

Use this to spot bloat; reclaim space from [Images](images.md) (prune dangling),
[Volumes](volumes.md) (prune unused) or the build cache.

## Running containers
A live table (refreshes automatically) of what's running, with quick
start/stop/restart actions. Click a name to open its
[detail page](containers.md). “View all →” goes to the full
[Containers](containers.md) list.

> Multi-host: the dashboard always reflects the host chosen in the sidebar
> switcher. Switch hosts to see another daemon's numbers.
