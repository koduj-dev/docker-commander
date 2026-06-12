# Changelog

All notable changes to Docker Commander are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/), and the project uses
[semantic versioning](https://semver.org/).

## [Unreleased]

### Added
- **Docker image autocomplete** — typing an image reference now suggests names
  and tags: in the **compose editor** (on `image:` lines) and in the **Create
  container** form. Suggestions blend the host's locally-pulled images (instant,
  offline) with a **Docker Hub** repository search (proxied through the host
  daemon, so no credentials leave the process) and, after a `:`, Docker Hub's
  **tag** list. Everything degrades to local-only when offline.
- **Builder — shared definitions (YAML anchors)** — the builder can now include
  reusable **top-level anchors** (e.g. `x-pg-common: &pg-common …`) emitted above
  `services:`, so a cluster of services can share one definition (security, cert
  mounts, …) and merge it with `<<: *pg-common`. Pick built-in ones (Service
  defaults, Secured Postgres) or save your own; they're managed on the Templates
  page like service blocks.
- **Templates management page** — a new **Templates** section (under Projects'
  permission) to manage presets, builder service blocks and shared definitions
  in one place: edit a user preset's files in the multi-file editor, rename it,
  add/edit/delete your own service blocks and shared definitions, inspect
  built-in ones read-only, and **duplicate** any preset, service block or shared
  definition (including built-ins) into a new editable copy. The **New project**
  dialog now shows a **live read-only preview** of the `compose.yml` a template
  or builder selection would produce, and the project editor opens wider.
- **Project templates & builder** — creating a project now offers three ways to
  scaffold it, all rendered server-side: **Template** (ready-made presets —
  Nginx static, Nginx + Postgres + Adminer, LEMP, Node + Postgres + Redis — with
  fill-in **variables** and auto-generated secrets), **Builder** (the *skládačka*:
  tick service blocks — Nginx, PHP, Node, Postgres, MySQL, Redis, Adminer — and
  they're merged into one compose), and **Import** (`.zip`). **Save as template**
  snapshots a project into a reusable preset, and you can add your own service
  blocks to the builder. Built-in presets/blocks are embedded; user-saved ones
  live in the data dir (the catalog is structured for a future remote source).
- **Self-install as a service** — `dockercmd --install-service` sets the binary
  up as a **systemd** service (Linux) or a per-user **launchd** LaunchAgent
  (macOS), with `--uninstall-service` and `--service-status`. Equivalent
  idempotent installer scripts also ship in `deploy/` (`install-linux.sh`,
  `install-macos.sh`, and `install-windows.ps1` via a Scheduled Task).

### Fixed
- **Bind-mounted project files were unreadable in containers** — seeded and edited
  project files were written `0600` / dirs `0700` owned by the service user, so a
  container running as a non-root uid (e.g. Nginx's worker, PHP-FPM's www-data)
  got `Permission denied` on bind-mounted files — the `nginx-static`/LEMP presets
  failed to serve `./html` / `./app`. Project files are now `0644` and their dirs
  `0755` (confinement stays at the data dir, which is `0700`). Existing projects
  created before this fix need to be re-created to pick up the new permissions.
- **Compose / Projects under systemd** — the `docker compose` CLI was reported as
  unavailable (Deploy/Down disabled, with a warning) when Docker Commander ran
  under the hardened systemd unit. `ProtectHome=true` makes the service user's
  `~/.docker` inaccessible, which breaks the docker CLI's plugin discovery; the
  unit now sets `DOCKER_CONFIG` to a writable path so the compose plugin is found.

## [1.3.0] — 2026-06-08

### Added
- **Self-update** — an admin **"update available"** banner that checks the GitHub
  Releases API against the running version (cached; `DC_UPDATE_CHECK=0` disables
  the outbound call), plus a **`dockercmd --self-upgrade`** command that downloads
  the right OS/arch asset, **verifies its SHA-256**, and atomically replaces the
  running binary. `--self-upgrade --check` reports whether an update is waiting
  without installing it.
- **Volume file browser** — browse, upload, download, delete and create folders
  inside a named volume (via a short-lived helper container, so it works on
  local / TCP / SSH hosts). **Upload & extract** a `.zip` / `.tar` / `.tar.gz`
  into a volume or container, and **seed a new volume** from an archive.
- **Project editor — real code editing & validation:**
  - A **CodeMirror 6** editor with YAML / JSON / shell / Dockerfile / `.conf`
    highlighting (replacing the bare textarea).
  - **Live, inline validation** of the *unsaved* buffer: compose via
    `docker compose config` (anchors, merge keys, `${VAR}` interpolation and
    `extends`/`include` resolved) shown as diagnostics on the right line;
    instant YAML / JSON / `.env` syntax lint; **Dockerfile** lint via
    `docker build --check`.
  - Compose **warnings** (unset variables), a **Resolved** preview (the fully
    flattened compose), and a **Summary** overview (services / ports / volumes +
    a duplicate-host-port check).
  - **Binary/data files** can live alongside the compose file (raw upload,
    download-only in the tree).
  - **New-project templates** — Nginx, Nginx + PHP-FPM, Postgres + Adminer, Node.
- **Networks — full management** — **create** (driver, subnet, gateway, internal,
  attachable), **connect** / **disconnect** containers, and **prune** unused
  networks; plus search / status filter on the list.
- **Topology at scale** — a **Find container** search, **filter by compose
  stack**, a **force-directed 2D layout** (instead of one tall column), a compact
  **list view** (with published ports), and a node-count badge. The network
  detail reuses the same graph/list renderers.
- **Confirmation dialogs** for every destructive action (delete / remove /
  prune), app-wide, replacing one-click and `window.confirm`.

### Changed
- Topology defaults to **running containers only** and **hides empty networks**;
  its filters persist across reloads. Edges are straight, animated lines.
- Anonymous (hash-named) volumes are shown shortened (full name on hover); the
  in-app confirm/prompt dialog is wider.

### Fixed
- Deterministic ordering for the container / network / volume / image / topology
  lists (tie-break beyond a case-folded name).
- Project file sandbox rejects a symlink anywhere along the path; archive extract
  guards against zip-slip and zip-bombs; the extract endpoints bound the request
  body. `ComposeAvailable` no longer caches a transient failure for the process
  lifetime.

## [1.2.0] — 2026-06-05

### Added
- **Compose stacks (discover & manage)** — a Stacks view that groups containers
  by their `com.docker.compose.project` label (so stacks started with the
  `docker compose` CLI show up too), with start / stop / restart / remove for a
  whole stack and a read-only **view of the stack's compose file** (read from
  the host — directly for the local daemon, over SSH for ssh hosts). A status
  LED, filter (name / service / image, by state), collapse/expand, and a
  cursor-following hover card with ports.
- **Compose projects** — create and edit a managed project *folder* (a compose
  file plus sidecar configs / scripts / init files) in a built-in multi-file
  **tree editor**, then **deploy it with the host's `docker compose` CLI** —
  including selecting **compose profiles** to enable. Import/export a project as
  a `.zip`, redeploy, and bring it down. Deployed projects appear on the Stacks
  page (lifecycle + view-compose reused) and link back and forth. Targets the
  local Docker host; Deploy/Down are disabled when the compose CLI isn't present.
- **Disable a host** — toggle a host off so the monitor ignores it entirely (no
  events stream, no stats sampling) and it's dropped from the host switcher —
  e.g. for a laptop/host that's offline. The Hosts page shows a `disabled` badge
  and an enable/disable button.
- App-wide UX: in-app confirm / prompt / alert dialogs (replacing the browser
  ones), each page header shows its section icon, and the sidebar logo links to
  the Dashboard.

### Fixed
- UI slowness on hosts with many containers: the dashboard resource overview no
  longer re-samples every container on demand (each `docker stats` call costs
  ~1s) — it reads the monitor's background snapshot, and the stats sweep runs
  less often. Also, an unreachable host no longer stalls the stats poll or spams
  reconnects (timeouts + exponential backoff; disable it to skip it entirely).
- Stable, alphabetical ordering for Containers (running first, then A→Z),
  Images, Volumes, Networks and Topology — they previously came back in the
  daemon's arbitrary order (which shuffled on reload).
- Dashboard "Open ports" no longer shows ports of containers that have since
  stopped — the cached scan is filtered to the currently-running containers and
  refreshes on Docker lifecycle events.
- Restored the pointer cursor on buttons (Tailwind v4 had dropped it).

### Changed
- Upgraded the frontend stack to current majors: **React 19**, **Vite 8**,
  `@vitejs/plugin-react` 6, **React Router 7**, **Tailwind CSS 4** (and the
  GitHub Actions to v6). No behavioural changes intended.

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

[1.3.0]: https://github.com/koduj-dev/docker-commander/releases/tag/v1.3.0
[1.2.0]: https://github.com/koduj-dev/docker-commander/releases/tag/v1.2.0
[1.1.0]: https://github.com/koduj-dev/docker-commander/releases/tag/v1.1.0
[1.0.0]: https://github.com/koduj-dev/docker-commander/releases/tag/v1.0.0
