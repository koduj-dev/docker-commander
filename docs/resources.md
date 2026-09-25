# Resources

[← Manual index](README.md)

**Observability → Resources** shows what each running container and each stack uses, which containers move the most network traffic, and what takes space on disk. It shows exact numbers you can sort and filter. The [Dashboard](dashboard.md) shows shares and summaries.

The page has four tabs: **Containers**, **Network**, **Stacks** and **Disk**. The active tab is in the URL as `?tab=` (for example `/resources?tab=network`). Without a tab, or with an unknown one, **Containers** opens. Everything is for the host selected in the sidebar switcher.

## How fresh the numbers are
- Containers and Stacks read the monitor's background snapshot. The page re-reads it every 5 s (the header shows *Updated hh:mm:ss · refreshes every 5 s*). The snapshot is re-sampled about every 15 s (`DC_METRICS_INTERVAL`, see [Deployment](deployment.md)). Two refreshes can show identical numbers. That is the same sample read twice. If a refresh fails, the last good table stays and the error shows above it.
- Network is recomputed from stored history every 15 s.
- Disk is cached and refreshed at most once a minute.

Only the Containers and Stacks tabs poll the snapshot.

## Containers
![Resources — containers](images/resources_containers.png)

Every running container and what it uses.

Four cards summarise the host: **Running containers**, **CPU** (cores in use, of how many, and % of the host), **Memory** (bytes in use, of total RAM, and %) and **Network** (received and sent, summed over all running containers).

The table has one row per container. Click a name to open its [detail page](containers.md).

| Column | Meaning |
|---|---|
| **CPU** | Cores in use, and the same as a % of the host (100% = every core) |
| **Memory** | Bytes in use, and the % of the host's total RAM |
| **Received / Sent** | Network rate in bytes per second, from the last two samples |

Click a column header to sort. Click again to flip the direction. The default is **Memory**, largest first. Ties sort by name. Search filters by container name. **Per page** offers 10, 20, 50 or 100. Your search text and page size are remembered.

A container that has just started or was just recreated shows no network rate until it has two samples.

## Network
![Resources — network](images/resources_network.png)

Containers ranked by network throughput over a window you choose.

- **Window**: *Last 5 min* (default), *Last 15 min* or *Last hour*.
- **Metric**: *Total (RX + TX)* (default), *Received* or *Sent*. This sets the sort order. Both **Received** and **Sent** columns are always shown.
- **Filter by container name**: applied before ranking, so you can find a container that is not in the top list.

Rows are numbered by rank, show the **Host** and link to the container. The list is capped at 50. If more containers qualify, a note says *Showing 50 of N — narrow the filter to find one outside this list*.

The figure is an average rate over the stored window. It is not a point-in-time sample and not a total transferred. Docker only exposes cumulative byte counters. The rate is the counter's increase between the first and last stored sample in the window, divided by the time between them. A counter that drops (the container was recreated) adds nothing.

Keep in mind:

- A container needs at least two stored samples inside the window. Otherwise it is left out. If nothing qualifies, the tab shows *Not enough history yet*. Wait a few minutes or pick a longer window.
- Only running containers are ranked.
- A container on several Docker networks has its traffic summed across all its interfaces. Docker's stats do not say which interface belongs to which network.
- Traffic between two containers counts twice: as TX for one and RX for the other.

The Dashboard's **Top talkers** panel is the top 10 of the *Last 5 min / Total* view. **View all →** opens this tab.

## Stacks
![Resources — stacks](images/resources_stacks.png)

What each Compose stack uses: the sum of its running containers' CPU, memory and network rates. **Running** shows running/total containers.

Click a row to expand it. You see the member containers, largest memory first, each linking to its detail page. A stack with nothing running says *No running containers*. Stopped containers add nothing.

The status filter defaults to **Running stacks**. Switch to **All stacks** to include stopped ones. Sorting, search and paging work as on Containers. Stacks come from the same API as [Stacks](stacks.md), so this tab also needs the **Containers** section (see Access).

## Disk
![Resources — disk](images/resources_disk.png)

What takes space on the host, largest first. The data comes from the daemon's `docker system df -v`.

Cards:

- **Reclaimable**: Docker's estimate of what a prune would free. It covers unused images (their unique layers), stopped containers' writable layers, unreferenced volumes and idle build cache. For images it is a lower bound. Layers shared only among several unused images are counted in none of them, although pruning all of them frees those too.
- **Volumes**: total size of volumes with a known size, the count, and how many are *unknown*.
- **Container writable layers**: data containers wrote on top of their images.
- **Build cache**: its size and how much is reclaimable. Records the daemon marks as *shared* are excluded from both.
- **Images**: the count, how many are unused, and the reclaimable bytes. There is no image size total, because image sizes include shared layers and would add up to too much.

Below the cards, switch between **Images**, **Containers** and **Volumes**. Each table has search, a per-page selector and sortable column headers.

| Section | Columns | Extra filter |
|---|---|---|
| **Images** | Name (tags, or the short id marked *untagged*), **Unique**, **Size**, **Used by** | *Unused only* |
| **Containers** | Name (with compose project), state, **Writable layer**, **Total** | *Stopped only* |
| **Volumes** | Name (with compose project), driver, **Size**, **Used by** | *Unused only* |

- **Size** (images) is the whole image, including layers shared with other images. **Unique** is what deleting this image would free. Images sort by **Unique** by default.
- **Total** (containers) is the writable layer plus the image. Containers sort by **Writable layer** by default.
- **Used by** is the number of containers using the image or volume, or *unused* (highlighted). If it could not be determined, it shows a dash and the item is not treated as unused.
- Volumes sort by **Size**, largest first.

**Unknown sizes.** If the daemon did not calculate a size, the table shows *unknown* instead of `0`. This happens for volumes from a non-local driver and for images whose shared size was not computed. Unknown sorts last in either direction and is left out of the card totals.

**Caching.** The report is cached per host for about a minute. The page reads it on open and then every minute. *As of hh:mm:ss* shows when it was generated. **Refresh** recomputes it, unless the report is younger than about 5 seconds. The first read on a big host can take a few seconds.

The Disk tab only reports. To free space, prune from [Images](images.md), [Volumes](volumes.md) or the build cache.

## Access
The sidebar entry and the `/api/stats/*` endpoints belong to the **dashboard** section, as on the [Dashboard](dashboard.md). Read access is enough. An account without that section does not see the entry and is refused if it calls the API directly. The **Stacks** tab also needs the **Containers** section.

Reads are checked against the host they name. A role [limited to specific hosts](users.md) sees these numbers only for hosts in its scope. An admin sees all hosts. An app-wide [disabled section](settings.md) hides the page for everyone.
