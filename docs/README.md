# Docker Commander — User manual

One page per part of the app, grouped roughly like the sidebar. The last section
covers installing and running it.

> New here? Start with **[Getting started](getting-started.md)**.
>
> Project home page: **[docker-commander.app](https://docker-commander.app)**.

## Workloads
- [Dashboard](dashboard.md) — host overview, disk usage, running containers
- [Containers](containers.md) — run, start/stop, console, files, logs, processes
- [Stacks](stacks.md) — Compose stacks: lifecycle, view and edit the compose file, redeploy
- [Projects](projects.md) — managed Compose folders: edit, validate, deploy, profiles, import/export
- [Templates](projects.md#managing-templates) — reusable presets, builder blocks and shared definitions, built-in and your own

## Storage
- [Images](images.md) — pull, build, push, tag, save/load/import, history, prune
- [Volumes](volumes.md) — create, inspect, remove, prune, browse files
- [Backup jobs](backup-jobs.md) — run your own backup command against volumes

## Network
- [Networks & Topology](networks.md) — create, connect, prune, and the connectivity graph

## Observability
- [Resources](resources.md) — CPU, memory and network per container and stack, top talkers, disk usage
- [Logs](logs.md) — live logs from many containers, regex search, parse rules
- [Events](events.md) — the live Docker event feed
- [Alerts](alerts.md) — rules and conditions, the feed, delivery records, webhooks, email, Prometheus

## System & administration
- [Hosts](hosts.md) — local, TCP+TLS and SSH daemons, host-key trust, per-host email
- [Registries](registries.md) — credentials for private pull and push
- [Users & roles](users.md) — accounts, permissions, read-only
- [Your profile](profile.md) — your account, second factors, sessions, what you can reach
- [Settings](settings.md) — feature flags, localhost 2FA, MCP tokens, LDAP, SMTP, data retention; also hosts the two pages below
- [Policy rules](policy-rules.md) — checks on project deploys (off / warn / block)
- [Recovery bundle](recovery.md) — export and import your setup
- [Audit log](audit.md) — record of privileged actions
- [Troubleshooting](troubleshooting.md) — health checks for the host's Docker setup
- [MCP (AI tools)](mcp.md) — control from AI tools (Claude Code/Desktop, Cursor): tokens, OAuth, allowed tools

## Operations
- [Getting started](getting-started.md) — first run, 2FA, the basics
- [Deployment](deployment.md) — running on a server: systemd, HTTPS, config, logs, health, self-update
- [Limits](limits.md) — every cap you can hit, and which can be changed
- [How it's tested](testing.md) — test tiers, multi-daemon runs over TCP/SSH, what isn't covered (for developers)
- [Gotchas](gotchas.md) — pitfalls in this codebase that cost real debugging time (for developers)
- [Dev environment](dev-environment.md) — local setup and the services the tests expect (for developers)
- [Changelog](../CHANGELOG.md) — what changed in each release

---

Screenshots live in [`docs/images/`](images/).
