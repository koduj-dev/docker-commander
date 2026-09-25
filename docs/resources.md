# Resources

[← Manual index](README.md)

**Observability → Resources** answers "how much does it take": what each running
container and each stack uses right now, which containers move the most network
traffic over the last few minutes, and what sits on disk. Where the
[Dashboard](dashboard.md) shows shares and summaries, this page shows the exact
numbers, sortable and filterable.

It has four tabs — **Containers**, **Network**, **Stacks** and **Disk**. The
active tab is kept in the URL as `?tab=` (`/resources?tab=network`), so a link
lands on it; `/resources` with no tab opens **Containers**, and an unknown value
falls back to it too. Everything is for the **host selected in the sidebar
switcher**.

## How fresh the numbers are
Three different clocks are involved, so it helps to know which one you are
looking at:

- **Containers and Stacks** read the monitor's background snapshot. The page
  re-reads it **every 5 s** (the header shows *Updated hh:mm:ss · refreshes every
  5 s*), but the snapshot itself is only re-sampled about **every 15 s**
  (`DC_METRICS_INTERVAL`, see [Deployment](deployment.md)). Two consecutive page
  refreshes can therefore show **identical numbers** — that is not a stuck page,
  it is the same sample read twice. A failed refresh keeps the last good table and
  shows the error above it.
- **Network** is not a snapshot at all — it is recomputed from stored history
  every 15 s (see below).
- **Disk** is a heavy daemon call, so it is cached and refreshed at most once a
  minute (see below).

Only the Containers and Stacks tabs poll the snapshot; switching to Network or
Disk stops that poll.

## Containers
![Resources — containers](images/resources_containers.png)

Every running container with what it actually takes.

The four cards on top summarise the host: **Running containers**, **CPU** (cores
in use, of how many, and as a % of the whole host), **Memory** (bytes in use, of
total RAM, and %) and **Network** (received ↓ and sent ↑, summed across all
running containers).

The table has one row per container — click the name to open its
[detail page](containers.md):

| Column | Meaning |
|---|---|
| **CPU** | cores in use, and the same as a % of the host (100% = every core) |
| **Memory** | bytes in use, and the % of the host's total RAM |
| **Received / Sent** | network rate from the last two samples, bytes per second |

Click a column header to sort; click it again to flip the direction. The default
is **Memory, largest first**; a new numeric column starts descending, **Container**
starts A→Z. Ties fall back to the name, so equal rows do not reshuffle on every
refresh. Search filters by container name and **Per page** picks 10 / 20 / 50 /
100; your search text and page size are remembered.

Network rates here are the change between the **two most recent samples**, so a
container that has only just started (or was just recreated) shows no throughput
until it has been sampled twice.

## Network
![Resources — network](images/resources_network.png)

Containers ranked by network throughput, over a window you choose.

- **Window** — *Last 5 min* (default), *Last 15 min* or *Last hour*.
- **Metric** — *Total (RX + TX)* (default), *Received* or *Sent*. This picks what
  the ranking is sorted by; both **Received** and **Sent** columns are always
  shown.
- **Filter by container name** — applied **before** ranking, so it can find a
  container that would not make the top of the list on its own.

Rows are numbered by rank, show the **Host** the container runs on, and link to
the container. The list is capped at **50**; when more containers qualify a note
says *Showing 50 of N — narrow the filter to find one outside this list*.

**What the number is.** It is an **average rate over the stored window**, not a
point-in-time sample and not a total transferred. Docker only exposes *cumulative*
byte counters, so the rate is the increase of the counter between the first and
last stored samples in the window, divided by the time between them. A counter
that drops (the container was recreated) contributes nothing rather than a
negative or inflated figure. A live per-poll ranking is deliberately not offered:
throughput is bursty enough that it would reorder itself on every sample.

A few consequences:

- A container needs **at least two stored samples inside the window** to appear.
  A container that just started, or a window with too little history behind it,
  is left out; if nothing qualifies you see *Not enough history yet* — wait a few
  minutes or pick a longer window.
- Only **running** containers are ranked.
- A container attached to several Docker networks has its traffic **summed across
  all its interfaces** — Docker's stats do not say which interface belongs to
  which network.
- Traffic between two containers counts on both sides (one's TX, the other's RX).

The Dashboard's **Top talkers** panel is the top 10 of the *Last 5 min / Total*
view; its **View all →** opens this tab.

## Stacks
![Resources — stacks](images/resources_stacks.png)

What each Compose stack takes in total: the **sum** of its running containers'
CPU, memory and network rates. The **Running** column shows *running/total*
containers.

Click a row (or its name) to expand it and see the member containers, largest
memory first, each linking to its detail page. A stack with nothing running says
*No running containers*. Stopped containers have no sample, so they simply add
nothing.

The status filter defaults to **Running stacks**; switch to **All stacks** to
include stopped ones. Sorting, search and paging work as on Containers. The stack
list is re-read every 5 s alongside the snapshot. Stacks are listed through the
same API as [Stacks](stacks.md), so this tab also needs the **Containers** section
(see below).

## Disk
![Resources — disk](images/resources_disk.png)

What takes the space on the host, largest first. It is built from the daemon's
`docker system df -v`.

The cards show:

- **Reclaimable** — Docker's own estimate of what a prune would free: unused
  images (their unique layers), stopped containers' writable layers, unreferenced
  volumes and idle build cache. For images it is a **lower bound** — layers shared
  only among several unused images are counted in none of them, though pruning all
  of them frees those too.
- **Volumes** — total size of the volumes whose size is known, with the count and
  how many are *unknown*.
- **Container writable layers** — the data containers wrote on top of their images.
- **Build cache** — its size and how much of it is reclaimable. Records the daemon
  marks as *shared* are excluded from both, because their layers are still
  referenced elsewhere and a prune would not free them.
- **Images** — the **count**, how many are unused, and the reclaimable bytes. There
  is deliberately no image size total on this page: every image's size includes the
  layers it shares with others, so adding them up would overcount.

Below, switch between **Images**, **Containers** and **Volumes** (each with a
count). Every table has search, a per-page selector and clickable column headers.

| Section | Columns | Extra filter |
|---|---|---|
| **Images** | name (tags, or the short id marked *untagged*), **Unique**, **Size**, **Used by** | *Unused only* |
| **Containers** | name (with its compose project), state, **Writable layer**, **Total** | *Stopped only* |
| **Volumes** | name (with its compose project), driver, **Size**, **Used by** | *Unused only* |

- **Size vs Unique** (images) — **Size** is the whole image, including layers
  shared with other images; **Unique** is what deleting *this* image would free
  (its size minus the shared layers). Images are sorted by **Unique** by default.
- **Total** (containers) — the writable layer plus the image. Sorted by
  **Writable layer** by default.
- **Used by** — the number of containers referencing the image or volume, or
  *unused* (highlighted) when there are none.
- Volumes are sorted by **Size**, largest first.

**Unknown sizes.** When the daemon did not calculate a size — typically a volume
from a non-local driver, or an image whose shared size was not computed — it shows
**unknown** rather than `0`, which would read as "empty". Unknown always sorts
**last**, in either direction, and is left out of the totals on the cards. A
**Used by** that could not be determined shows a dash and is not treated as unused.

**Caching.** Walking every image, container and volume is heavy on the daemon, so
the report is **cached per host for about a minute**. The page reads it on open and
then again every minute; *As of hh:mm:ss* shows when it was generated. **Refresh**
recomputes on demand — but a report younger than about **5 seconds** is reused, so
double-clicking does not hammer the daemon. The first read on a big host can take
a few seconds.

To actually free the space, prune from [Images](images.md), [Volumes](volumes.md)
or the build cache. The Disk tab only reports; nothing on it deletes.

## Access
The Resources page is gated like the [Dashboard](dashboard.md): the sidebar entry
and the underlying `/api/stats/*` endpoints belong to the **dashboard** section,
and **read** access is enough — there is nothing to write here. An account without
that section does not see the entry and is refused if it calls the API directly.
The **Stacks** tab additionally lists stacks, which needs the **Containers**
section.

Reads are checked against the host they name, so a role
[limited to specific hosts](users.md) can see these numbers only for hosts in its
scope. As everywhere, an admin can see all hosts, and an app-wide
[disabled section](settings.md) hides the page for everyone.
