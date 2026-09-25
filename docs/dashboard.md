# Dashboard

[← Manual index](README.md)

![Dashboard](images/dashboard.png)

The landing view summarises the **selected host**.

## Host facts
Top row of cards: hostname + Docker version, CPU count + architecture, total
memory + OS, and counts of **running** / **stopped** containers and **images**.

## Disk usage
A row of four tiles from `docker system df`, each showing a size and a count:

- **Images** — the daemon's own image total, with the number of images. Layers
  shared between images are counted **once**, so a host of images built on one
  base is not inflated by the base's size times the image count.
- **Containers (rw)** — the combined writable layers of all containers, with the
  container count.
- **Volumes** — the combined size of volumes whose size is known, with the volume
  count.
- **Build cache** — the cache size and its number of records. Records the daemon
  marks as *shared* are left out of both the size and the count, since their
  layers are still referenced elsewhere.

Use this to spot bloat. For the per-object breakdown — which image, container or
volume takes the space, and what a prune would reclaim — open
[Resources → Disk](resources.md#disk). To reclaim space, use [Images](images.md)
(prune dangling), [Volumes](volumes.md) (prune unused) or the build cache.

## Resource usage
Three panels: **CPU** and **memory** as a share of the host, and **network** as
current throughput.

The two pie charts show how the **running containers** divide up the host's CPU
and memory — i.e. what slice of the whole machine each container is using right
now, with the unused remainder shown as **Free**. The busiest containers get their
own slice; the rest are grouped as **Other**.

> CPU share is relative to all cores (100% = the entire host); memory share is
> usage ÷ total RAM. Remote hosts work the same, over the Docker API.

**Network · all containers** is the host-wide RX/TX rate right now, with a short
rolling trend beside it. It is deliberately *not* a pie or a per-container
ranking: a pie claims "parts of a whole", and the only whole available is
whatever happens to be moving — so one container at 100% of 2 KB/s would look
exactly like one at 100% of 800 MB/s. A *live* ranking is no better either,
because throughput is bursty enough to reorder itself on every poll — which is
why per-container ranking lives in its own **Top talkers** panel instead (below),
sourced differently. Per-container series live on the
[container detail](containers.md#network).

Summed across running containers, so **container-to-container traffic counts
twice** — once as one side's TX and once as the other's RX.

All three panels re-sample on Docker lifecycle events and on a slow poll, updating
in place; a transient error keeps the last good numbers rather than blanking the
section.

## Top consumers
Beside the pies, a table of the running containers that use the most, with
**CPU** (cores in use) and **Memory** (bytes in use) per container. Click a column
header to re-sort — the default is memory, largest first. It shows the top 10 (the
title says *10 of N* when there are more); **View all →** opens
[Resources](resources.md) for the full, searchable list. Click a name to open the
container's [detail page](containers.md).

## Top talkers
A ranked list of the busiest containers by network throughput, next to **Top
consumers** (each takes at most half the width, and the two share the same table
design). Unlike the network summary above, this is not a live snapshot: ranking
containers by a point-in-time poll sample is unreadable (throughput is bursty
enough to reorder itself every 8–15 seconds), so this ranks by the **average rate
over a stored window** instead — the last 5 minutes, by total (received + sent),
top 10, refetched every 15 seconds.

There is no separate Top talkers page. **View all →** opens
[Resources → Network](resources.md#network), which has a bigger table, a window
selector (5 min / 15 min / 1 hour), a metric selector (total, received, sent) and
a name filter. Both share the same limitation as the rest of the app's network
reporting: a container attached to more than one Docker network has its traffic
summed across all of its interfaces, since Docker's own stats API does not say
which interface belongs to which network.

A container needs at least two samples inside the window to show up at all — a
container that just started, or a window with too little history behind it, is
left out, and if nothing qualifies the panel says "Not enough history yet".

## Open ports
A host-wide map of every **published port** across the running containers.
**Scan** needs **write** access to the *dashboard* section, because it is an
active network action rather than a lookup — a read-only account can see the port
map but not launch the probe. It actively connects to each one and fingerprints
what's really listening
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
