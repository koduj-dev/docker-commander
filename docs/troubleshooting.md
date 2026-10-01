# Troubleshooting

[← Manual index](README.md)

A quick health check of the selected host's Docker setup. It looks for the
problems that quietly break things: overlapping networks, port clashes, logs
that grow forever, a nearly full disk. Every check is read-only and nothing is
changed for you.

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

**A container won't start: "port is already allocated".** **Duplicate port
bindings** shows which running containers already hold the port.

## The checks

| Check | Fails or warns when |
|---|---|
| **Docker network subnet overlap** | Two Docker networks use overlapping subnets (fail). |
| **Docker network vs. host network overlap** | A Docker network overlaps a real, non-Docker interface on the host (fail). |
| **MTU mismatch** | A bridge network's MTU differs from the host's default-route interface (warning). |
| **Duplicate port bindings** | The same host port is bound by more than one running container (fail), or published on more than one host IP (warning). |
| **Log rotation** | A running container logs with `json-file` and has no `max-size` (warning). Other log drivers manage their own rotation. |
| **Host disk space** | Free space at Docker's data directory is below 15 % (warning) or 5 % (fail). |
| **Dangling networks & volumes** | Unused networks or volumes exist (warning). |

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
is recorded in the [audit log](audit.md) as `diagnostics.run`.
