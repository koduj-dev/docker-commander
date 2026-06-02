# Volumes

[← Manual index](README.md)

List volumes with driver, scope and mountpoint. Each volume shows **which
containers mount it** (cross-referenced from container mounts) so you know what
you'd affect before removing one. Search and paginate as elsewhere.

## Actions
- **Create** — a named volume with an optional driver (defaults to `local`).
- **Inspect** — raw JSON (driver options, labels, mountpoint…).
- **Remove** — delete a volume; a **force** fallback appears if the daemon
  refuses it (e.g. still referenced). The daemon will not remove a volume that
  is actively mounted by a running container.
- **Prune unused** (header) — remove all volumes not used by any container.

## Tips
- A volume marked **in use by …** is mounted by those containers — stop/remove
  them first, or use the volume inspector to understand the dependency.
- Reclaimable volume space is summarised on the [Dashboard](dashboard.md).
