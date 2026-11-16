# Troubleshooting

[← Manual index](README.md)

A quick health check of the selected host's Docker setup. It looks for the
problems that quietly break things: overlapping networks, port clashes, logs
that grow forever, a nearly full disk. Every check is read-only and nothing is
changed for you.

Admins also see a **Docker Commander** group above the host checks: how Docker
Commander itself is installed and what its environment stops it from doing (see
[Docker Commander checks](#docker-commander-checks)).

The checks run as soon as you open the page. **Run diagnostics** runs them again.
Each result is **OK**, **Warning**, **Failed** or **Skipped**. A warning or
failure opens with its details, for example the names of the affected
containers.

## Common tasks

**Containers can't reach part of your LAN or VPN.** Look at **Docker network
vs. host network overlap**. A Docker network using the same range as a real
interface (a VPN, a LAN) captures that traffic. Move the Docker network to
another subnet.

**Large downloads or TLS connections hang inside containers.** Look at the
**MTU** check. A bridge network whose MTU doesn't match the host's real interface
can drop big packets while small requests still work.

**The disk keeps filling up.** Check **Log rotation** first. It lists containers
that log with `json-file` and no `max-size`, so their logs grow until the disk is
full. Then check **Host disk space** and **Dangling networks & volumes**.

**A stack's compose file says "permission denied" or opens read-only.** Look at
**Local stack compose files** in the Docker Commander group. It lists every local
stack whose file Docker Commander can't read or can't save, with the cause (the
systemd sandbox hiding `/home`, another owner, a read-only path) and the fix.

**The update banner has no Update & restart button.** **Installation and updates**
says why (package install, container, unwritable directory, or turned off) and
which command updates instead.

**A container won't start: "port is already allocated".** **Duplicate port
bindings** shows which running containers already hold the port.

## The checks

| Check | Fails or warns when |
|---|---|
| **Docker network subnet overlap** | Two Docker networks use overlapping subnets (fail). |
| **Docker network vs. host network overlap** | A Docker network overlaps a real, non-Docker interface on the host (fail). |
| **MTU mismatch** | A bridge network with an explicitly set MTU (`com.docker.network.driver.mtu`) differs from the host's default-route interface (warning). Bridge networks that leave the MTU at Docker's default aren't compared. |
| **Duplicate port bindings** | The same host port and protocol on the same host IP is bound by more than one running container (fail), or the same port is published on more than one host IP (warning). |
| **Log rotation** | A running container logs with `json-file` and has no `max-size` (warning). Other log drivers manage their own rotation. |
| **Host disk space** | Free space at Docker's data directory is below 15 % (warning) or 5 % (fail). |
| **Dangling networks & volumes** | Unused networks or volumes exist (warning). |

### Docker Commander checks

Admins only. They describe this Docker Commander process, not the selected host,
and run even when the host's checks fail (for example when the Docker socket is
off limits, which is one of the things they catch).

| Check | Fails or warns when |
|---|---|
| **Installation and updates** | Update & restart can't run because the binary's directory isn't writable (warning). A package install, a container, `DC_SELF_UPDATE=0` and Windows are OK, with the way to update instead. Details name the version, the binary, the service user and whether systemd or a container started it. |
| **Service sandbox** | Never fails: it reports what the systemd unit hides or makes read-only (`ProtectHome`, `ProtectSystem=strict`), so the failures below make sense. |
| **Data directory** | The data directory, an entry in it, or a per-project directory isn't writable (fail), typically files left owned by root after a restore run with `sudo`. Shows the `chown` that fixes it. Looks at up to 500 paths. |
| **Local Docker access** | The local Docker daemon can't be reached (fail), with "permission denied" on the socket explained as a missing `docker` group membership. |
| **Local stack compose files** | A local stack's compose file can't be read (warning). Files that can be read but not saved are listed too; they open read-only and still redeploy. Stacks on SSH hosts are checked when opened. |
| **Command-line tools** | `docker compose` isn't found (warning): Projects can't deploy and stacks can't redeploy. Also lists `docker buildx` (Dockerfile checks) and the optional `trivy` (image Scan). Each is looked up once per process, so restart after installing one. |

Every check only reads: it opens, lists and asks `access(2)`, and never writes a
file. The run is audited as `diagnostics.run` with target `instance`.

### Technical notes
- **Host checks need a shell.** The interface, MTU and disk checks look at the
  host itself: directly for the local daemon, over SSH for an [SSH host](hosts.md).
  A plain-TCP host has no shell to reach, so those checks show **Skipped** with
  the reason.
- **Log rotation** inspects at most the first 500 running containers, and says so
  when it stops there.
- The disk thresholds are fixed and not configurable.

## Permissions
The page belongs to the **diagnostics** section. Running the checks needs
**write** access, because on remote hosts they run commands over SSH. Every run
is recorded in the [audit log](audit.md) as `diagnostics.run`. The Docker Commander group is
for admins only: it names the binary, the service user, the data directory and
local stack paths, which belong to the installation rather than to a host a
**diagnostics** grant covers.
