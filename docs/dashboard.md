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

## Resource usage (share of host)
Two pie charts show how the **running containers** divide up the host's **CPU**
and **memory** — i.e. what slice of the whole machine each container is using
right now, with the unused remainder shown as **Free**. The busiest containers
get their own slice; the rest are grouped as **Other**. It's a snapshot taken
when the page loads (sampling every container isn't free, so it doesn't poll).

> CPU share is relative to all cores (100% = the entire host); memory share is
> usage ÷ total RAM. Remote hosts work the same, over the Docker API.

## Open ports
A host-wide map of every **published port** across the running containers.
**Scan** actively connects to each one and fingerprints what's really listening
(SSH / HTTP(S) / SMTP / Redis / TLS / banner) — not just a guess from the port
number. It only runs on demand (probing is an active network action), works for
remote hosts too (SSH ports are tunnelled), and only touches **your own** hosts.

## Running containers
A live table (refreshes automatically) of what's running, with quick
start/stop/restart actions. Click a name to open its
[detail page](containers.md). “View all →” goes to the full
[Containers](containers.md) list.

> Multi-host: the dashboard always reflects the host chosen in the sidebar
> switcher. Switch hosts to see another daemon's numbers.
