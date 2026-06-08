# Docker Commander — User manual

A guide to each part of the app. Most pages map 1:1 to a menu item; the last
two cover installation and configuration.

> New here? Start with **[Getting started](getting-started.md)**.

## Compute
- [Dashboard](dashboard.md) — host overview, disk usage, running containers
- [Containers](containers.md) — create/run, lifecycle, console, files, logs, processes
- [Stacks](stacks.md) — Compose stacks: discover, lifecycle, view compose file
- [Projects](projects.md) — managed Compose folders: edit + live validation, deploy, profiles, templates, import/export
- [Images](images.md) — pull, build, push, tag, save/load/import, history, prune
- [Volumes](volumes.md) — list, inspect, create, remove, prune, browse files

## Network
- [Networks & Topology](networks.md) — manage networks (create/connect/disconnect/prune) and the connectivity graph

## Observability
- [Logs](logs.md) — aggregated streaming, regex search, structured parse rules
- [Events](events.md) — the live Docker event feed
- [Alerts](alerts.md) — rules, webhooks, email, Prometheus

## System & administration
- [Hosts](hosts.md) — local / TCP+TLS / SSH daemons, host-key trust, per-host email
- [Registries](registries.md) — stored credentials for private pull & push
- [Users & roles](users.md) — accounts, permissions, read-only
- [Settings](settings.md) — feature flags, localhost 2FA, LDAP
- [Audit log](audit.md) — record of privileged actions

## Operations
- [Getting started](getting-started.md) — first run, 2FA, the basics
- [Deployment](deployment.md) — running on a server (systemd, HTTPS, config, logs, health, self-update)
- [Changelog](../CHANGELOG.md) — what changed in each release

---

Screenshots used throughout live in [`docs/images/`](images/).
