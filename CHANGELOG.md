# Changelog

All notable changes to Docker Commander are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/), and the project uses
[semantic versioning](https://semver.org/).

## [Unreleased]

### Added
- **Compose stacks (discover & manage)** — a Stacks view that groups containers
  by their `com.docker.compose.project` label (so stacks started with the
  `docker compose` CLI show up too), with start / stop / restart / remove for a
  whole stack and a read-only **view of the stack's compose file** (read from
  the host — directly for the local daemon, over SSH for ssh hosts). Deploying a
  stack from a compose file comes next.

### Fixed
- Stable, alphabetical ordering for Containers (running first, then A→Z),
  Images, Volumes, Networks and Topology — they previously came back in the
  daemon's arbitrary order (which shuffled on reload).
- Dashboard "Open ports" no longer shows ports of containers that have since
  stopped — the cached scan is filtered to the currently-running containers and
  refreshes on Docker lifecycle events.

### Project / infrastructure
- Community health files: Code of Conduct, contributing guide, security policy,
  and issue / pull-request templates.
- Dependabot for Go modules, npm and GitHub Actions (weekly, grouped).

## [1.1.0] — 2026-06-04

### Added
- **Host detail** — an info panel per host (hardware, OS/kernel, Docker engine
  config, container/image counts), with a note that Docker Desktop reports its
  Linux VM, not the desktop OS.
- **Discoverable host switching** — the sidebar "Viewing host" switcher is now
  prominent and every page header shows the active host, so multi-host views are
  clearly separated.
- **2FA choice at first-run setup** — enable 2FA now (enrollment follows) or
  defer it (localhost stays password-only; toggle it later in Settings).
- **Config file** — settings can live in `/etc/docker-commander/commander.conf`
  (`%ProgramData%\…` on Windows); override with `-config` / `$DC_CONFIG`.
  Precedence: flag → env → file → default.
- **Listen address as host + port** — `DC_HOST` / `DC_PORT` (and a `-p`
  shorthand); the full `DC_ADDR` is kept as a legacy override.
- **Native HTTPS** — set `DC_TLS_CERT` + `DC_TLS_KEY` to serve TLS directly,
  without a reverse proxy.
- **Dashboard: resource breakdown** — pie charts of each running container's
  share of the host CPU and memory.
- **Dashboard: open-ports scan** — a host-wide map of published ports with
  **active service fingerprinting** (SSH / HTTP(S) / SMTP / Redis / TLS /
  banner); SSH hosts are probed through their tunnel. Per-container probing is
  also available on the container detail page.
- **Health & version** — unauthenticated `GET /healthz` (alias `/health`) for
  load balancers / k8s; build version shown in the sidebar footer and at
  `GET /api/version`.
- **Alerts in the system log** — every fired alert is written to stderr as a
  structured line, so failures reach the journal / syslog, not just the in-app
  feed.
- **List filters** — status filters (running·stopped, in-use·unused) for
  Containers, Images and Volumes.
- **Per-user preferences** — list filters, status and page size are stored
  server-side, so they follow the account across browsers.
- **Audit pagination** — search + prev/next paging over the audit log.
- **Scroll restoration** — returning from a detail page lands where you were.
- **Near-real-time dashboard** — refreshes are driven by the Docker events
  stream, so containers starting/stopping show up almost immediately.

### Changed
- Default listen port `8080` → **`8470`** (less likely to collide).
- Configuration consolidated on the single config file (the separate systemd
  env example was removed).
- Stronger guidance for remote hosts: prefer **SSH**, and TLS/firewall warnings
  for exposing the Docker daemon over TCP.

### Fixed
- **SSH hosts now connect** — the Docker-over-SSH transport failed with
  `lookup docker.ssh … no such host` because `client.WithHost` clobbered the
  tunnel's `DialContext`; option order is fixed.
- **Dashboard no longer crashes** when no containers are running (Go `nil` slice
  → JSON `null` → `null.length`).
- **Remote port scans no longer hang** — the SSH-tunnelled dialer now honours a
  timeout.
- Dark-theme `<select>` dropdowns no longer render white-on-white.
- Pie-chart tooltip text is readable on the dark theme.
- The resource-usage section reserves its space (no layout jump) and shows
  errors in place of the charts.

### Tooling / tests
- Added a unit + integration test suite (~66% coverage). CI runs `go test
  -short` (deterministic); Docker/Redis/LDAP/SMTP integration tests are gated
  behind `testing.Short()`. GitHub Actions bumped to the Node-24 majors.

## [1.0.0] — 2026-06-02

Initial release: a single CGO-free Go binary with an embedded React UI.

- **Monitoring** — live CPU/memory graphs, historical charts (Redis/in-memory),
  aggregated logs with level detection, regex search and parse rules, events
  feed, diff/top/df, raw inspect.
- **Control** — full container lifecycle, in-container file browser (`docker
  cp`), images (pull/build/push/tag/save/load/import/history/prune), volumes &
  networks CRUD, interactive shell.
- **Multi-host** — local / TCP+TLS / SSH daemons with verified host keys.
- **Alerting** — state / resource / log / restart rules → webhooks, email
  (SMTP, per-host), in-app feed, Prometheus `/metrics`.
- **Security & admin** — Argon2id + TOTP 2FA, multi-user with roles /
  per-section permissions / read-only, feature flags, audit log, optional LDAP;
  secrets encrypted at rest.

[1.1.0]: https://github.com/koduj-dev/docker-commander/releases/tag/v1.1.0
[1.0.0]: https://github.com/koduj-dev/docker-commander/releases/tag/v1.0.0
