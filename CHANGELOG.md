# Changelog

All notable changes to Docker Commander are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/), and the project uses
[semantic versioning](https://semver.org/).

## [Unreleased]

### Added
- **A fallback role for LDAP mappings.** A mapping can name a role that has since
  been deleted; until now its members simply got nothing. Nominate a fallback in
  *Settings → LDAP* and they degrade to that baseline (**Viewer** being the obvious
  choice) instead. The nominated role can't be deleted while it holds the job, and
  the two built-in roles were never deletable, so the fallback itself can't go
  stale.

  It applies only to a mapping that **matched and then failed to resolve** — never
  to a user whose groups map to no role at all. That case is "not entitled", and
  granting a baseline there would hand a role to every account in the directory that
  can authenticate.

- **LDAP groups can grant named roles**, not just raw sections. A group mapping now
  carries roles, so directory membership drives access the same way it drives
  everything else in an AD shop: put someone in `cn=deployers`, they hold
  **Deployer** on their next login; take them out, they lose it. Roles are
  re-derived on **every login**, so revocation doesn't wait for a session to
  expire. This completes phase 1 of
  [design/rbac-roles-and-host-scoping.md](design/rbac-roles-and-host-scoping.md).

  Two limits are deliberate and tested: a mapping **can never grant admin** (only
  the admin group DN does that, and role management isn't reachable through any
  role), and a role id left behind by a **deleted role grants nothing** rather than
  failing the login.

  Upgrading changes nothing on its own — roles become directory-driven only once at
  least one mapping actually grants a role, so a config written before this release
  keeps whatever roles an admin assigned by hand. Sections keep their existing,
  stricter rule: any mapping at all makes LDAP authoritative for them.

- **A profile page** (person icon beside *Sign out*) with three tabs. **Account** —
  who the installation thinks you are (username, account type, whether you sign in
  locally or through LDAP, created and last-seen), and your **alert e-mail**.
  **Security** — 2FA status and *Pair a new authenticator*. **Access** — the roles
  you hold and a table of every section you can reach, whether you can change it,
  and **which role each permission came from**, which was previously invisible to
  anyone but an admin. It replaces the small e-mail dialog.

  All of it is self-service and reads only your own account; role management stays
  admin-only.

### Fixed
- **Starting a 2FA re-pair no longer disables the authenticator you already have.**
  Beginning enrolment overwrote the live secret and switched 2FA off, so abandoning
  the flow silently left the account without 2FA *and* invalidated the authenticator
  in the user's hand. Harmless while first-time enrolment was the only caller;
  the profile page's *Pair a new authenticator* makes it an everyday path. The new
  secret is now held aside and only takes over once a code from the new device is
  accepted — cancel, close the tab or type the wrong code and nothing changes.
- **Alert e-mails go where you want them.** An alert rule can now carry **its own
  recipients** instead of every rule mailing the one instance-wide address. Each
  account also gets an **alert e-mail** it can set itself (the person icon beside
  Sign out), which prefills as the recipient the first time you switch e-mail on
  for a rule — so the common "tell me about my own alerts" case needs no typing.

  Recipients resolve most-specific-first: the rule's own list, then the host's
  `alert_email` override, then the instance-wide SMTP *To*. Rules created before
  this have no list, so they deliver exactly as they did. When LDAP publishes a
  `mail` attribute it is synced to the account on login; a blank attribute never
  clears an address set by hand, since silently losing it would stop alerts
  arriving.
- **Backup & restore** — `dockercmd --backup <file>` writes a complete, portable
  snapshot of the installation (database + `projects/` + `project-templates/`), and
  `dockercmd --restore <file>` puts it back. The snapshot is taken through a live
  connection with `VACUUM INTO`, so it is **safe to run while the server is up** —
  copying the `.db` yourself is not, since it runs in WAL mode.

  Both secret keys live inside the database, so a backup restores onto a fresh
  machine as-is — and, for the same reason, the archive is equivalent to the
  plaintext of every stored secret. It is written `0600`, and **`--passphrase`
  encrypts it** (AES-256-GCM with an Argon2id-derived key). The passphrase is read
  from the terminal, or from stdin when piped so scheduled backups can be encrypted
  too; it is never passed as an argument, where it would land in shell history and
  `/proc/<pid>/cmdline`. Restore refuses to overwrite an existing installation
  without `--force`, and every archive entry is jailed to the data dir.
- **Named RBAC roles** — a role is a reusable bundle of section grants, so an
  admin no longer ticks thirteen checkboxes per account. Each section in a role is
  independently **read-only or writable**, which is finer-grained than the
  account-level read-only flag. Two roles ship built in and are immutable —
  **Viewer** (every section, read-only) and **Operator** (day-to-day work, but
  deliberately not `hosts` / `registries` / `audit`) — with **Duplicate** to make
  an editable copy, mirroring project templates. A user may hold several roles and
  still have per-account sections; effective access is the union.

  Existing accounts are unaffected: with no roles assigned, permissions resolve
  exactly as before. The precedence is explicit and tested — admins bypass; grants
  union; the **account-level read-only flag caps everything** so a writable role
  cannot lift it; and an app-wide **disabled section wins over any role**. Role
  management is **admin-only** (no combination of section grants reaches it) and
  every change is audited. Changes apply on the user's next request — nothing is
  cached in the session, so revoking a role is immediate.

  **Managed from the UI:** *Users* becomes **Users & roles** with an Accounts /
  Roles tab pair. The Roles tab lists each role with its grants, how many accounts
  hold it, and a built-in/yours badge; built-ins open read-only with **Duplicate**
  to make an editable copy. The role editor sets every section to *—* / *read* /
  *write* with all thirteen visible at once — no scrolling to check you didn't
  grant `hosts`. Accounts gained **Roles** and **Access** columns, roles are
  assignable in the create/edit forms, and an account whose grants are all
  read-only is labelled *view only* even without the read-only flag.

  _This is phase 1 of [design/rbac-roles-and-host-scoping.md](design/rbac-roles-and-host-scoping.md); per-host scoping is phase 2._

### Security
- **The SMTP config is now admin-only, and lives under Settings → Email.**
  It is a *single instance-wide* outbound mail relay with a stored credential, but
  it was gated by the **alerts** section — so any non-admin with write access to
  alerts could repoint the whole installation's mail at a server they control and
  receive its notifications. (The password was never returned by the API, so this
  was about redirecting delivery, not reading the secret.) `/api/smtp` and
  `/api/smtp/test` now require admin.

  **This narrows an existing permission:** someone who managed alerts *including*
  their e-mail delivery will now need an admin to configure the relay. The rest of
  the alerts surface — feed, rules, webhooks — is unchanged for them, and alert
  rules can still opt into e-mail.
- **Raw `inspect` no longer readable without the owning section.**
  `GET /api/inspect/{kind}` returns the **raw** Docker inspect payload, which for a
  container includes `Config.Env` — database passwords, API keys. It was ungated,
  so **any signed-in account** (no sections granted at all, even read-only) could
  read the environment of any container on **any** host via `?host=`. It is now
  gated by the section that owns the kind: container→`containers`, image→`images`,
  volume→`volumes`, network→`networks`, with an unknown kind failing closed onto
  `containers`. This matches how the UI already uses it — the Inspect dialog only
  opens from pages those sections already guard — so nothing legitimate changes.
- **MCP tokens: a scope that narrowed to nothing produced an *unrestricted*
  token.** An empty stored scope means "inherit the owner's rights", so requesting
  a scope consisting only of sections the account doesn't hold was silently turned
  into a token with the owner's **full** access — asking for less returned more.
  Such a request is now refused. Token scopes are also matched against
  **effective** sections, so access granted through a role can be scoped to (it
  would otherwise have been dropped, hitting the same widening path). This was
  reachable before roles existed, by requesting only ungranted sections.
- **Remote Projects now support bind mounts.** Deploying a managed project to a
  remote host previously refused any compose file with a host-path bind mount,
  because the remote daemon can't see Docker Commander's data dir. Each bind
  whose source lives **inside the project folder** is now copied to a named
  volume on the target host (`dcseed-<project>-<hash>`, labelled with the
  project) and the mount is repointed at it via a generated compose override, so
  sidecar configs and scripts work remotely — including single-file mounts like
  `./nginx.conf:/etc/nginx/nginx.conf`, which mount out of the volume by
  subpath. `read_only` is preserved and the project's own named volumes are left
  untouched. The copy is a **snapshot taken at deploy time, not a live mount**:
  editing the files needs a redeploy, and writes inside the container stay on the
  remote host. The deploy output says so explicitly.
- Bind mounts pointing **outside** the project folder (e.g. `/etc/localtime`,
  `/var/run/docker.sock`, or anything reached through a symlink out of the
  folder) are still **refused** on a remote deploy, now with a message naming
  them: they address paths on the remote host, which won't be mounted blind.

- **Remote deploys can opt into host paths.** Bind mounts pointing outside the
  project folder are still refused by default, but a project's Settings now has
  **Allow host paths** to mount them from the remote host's own filesystem
  instead (contents are whatever exists there; nothing is copied, and the deploy
  output names them every time). Enabling it needs **write access to the Hosts
  section** — it is authority over the host, not the project — and is audited;
  turning it back off needs only project access, so a restricted user can always
  close a hole they cannot open.
- **Deleting a project offers to remove its seeded volumes.** A remote deploy
  leaves `dcseed-*` volumes on the target host, which used to accumulate
  silently. The delete flow now lists them and asks, since they hold data;
  declining keeps them for a later redeploy. Only volumes carrying that
  project's seed label are touched.

### Changed
- **Settings and MCP Admin are now tabbed**, using the same shared tab bar as
  Alerts, Templates and the container detail view: Settings splits into
  **Features / Security / LDAP / Email**, and MCP Admin into **API tokens / OAuth
  clients**. The SMTP form moved from Alerts → Settings → Email (see Security
  above); Alerts keeps Feed / Rules / Webhooks.
- **Changing a deployed project's target host now warns first.** It does not tear
  the project down on the old host, so you would end up with two live copies
  while the page showed only the new one. The host picker says so inline and asks
  for confirmation before saving.

### Documentation
- **`docs/hosts.md` now covers two SSH-host requirements** that produced
  confusing failures. The remote `sshd` must allow forwarding
  (`AllowTcpForwarding`) because the Docker API is tunnelled over an SSH channel
  — Alpine ships it as `no`, and `sshd` honours the *first* occurrence of a
  keyword, so appending to `sshd_config` is silently ignored. This otherwise
  yields a half-working host: `docker compose` uses its own `dial-stdio` channel
  and needs no forwarding, so **Projects deploys succeed while monitoring
  fails**. Also noted that an agent holding several keys can exhaust `sshd`'s
  `MaxAuthTries` before the right key is offered.
- **New [How it's tested](docs/testing.md) page** — the app is pointed at real
  Docker daemons, so this spells out the five test tiers (unit, adversarial
  "pentests", runtime smoke, integration against a real daemon, and multi-daemon
  end-to-end over TCP and SSH), how to run each, and — deliberately — **what is
  not covered**: CI runs only the deterministic tiers, there's no browser/UI
  suite, and coverage is unit-only. Linked from the README and CONTRIBUTING.

### Removed
- The **Go Report Card badge**, which now renders "go report: retired" since the
  service shut down. It returned HTTP 200, so it displayed a misleading grade
  rather than failing visibly as a broken image.

## [1.5.1] — 2026-07-30

### Changed
- **Release signatures are now a cosign bundle.** The release workflow moved to
  cosign v3, which writes a single Sigstore bundle instead of a detached
  signature + certificate pair, so releases now ship `SHA256SUMS.bundle` in place
  of `SHA256SUMS.sig` / `SHA256SUMS.pem`. Verify with
  `cosign verify-blob --bundle SHA256SUMS.bundle … SHA256SUMS` (needs **cosign
  v3+**); releases up to v1.5.0 keep the old pair. The workflow now also asserts
  the bundle exists before publishing, so a signing regression fails the release
  instead of silently shipping unsigned assets.
- **Dependencies updated** — Go modules (incl. `golang.org/x/crypto`,
  `modernc.org/sqlite`, `go-chi`, `go-ldap`, `go-redis`, `coder/websocket`),
  frontend packages (incl. React, Vite, Recharts, `@xyflow/react`) and the
  pinned GitHub Actions.

### Fixed
- **`CHANGELOG.md` — restored the missing `[1.5.0]` heading.** The v1.5.0 entries
  had been left sitting under `[Unreleased]` with no version heading of their own,
  which also orphaned the existing `[1.5.0]` link definition.

## [1.5.0] — 2026-06-16

### Added
- **Image vulnerability scanning** — the Images page gains a **Scan** action that
  runs [Trivy](https://trivy.dev) against an image on the selected host and shows
  a severity summary plus a CVE table (package, installed vs. fixed version,
  advisory link). Trivy is an optional dependency probed at runtime; when it's
  absent the dialog says how to install it. Scans run live (not persisted). The
  image reference is validated before it reaches the CLI (no argument injection).
- **Remote Projects** — a managed Compose project can now target a **remote
  Docker host** (added under Hosts), not just the local daemon. Pick the host at
  create time or in the project's Settings; deploy/down/restart run
  `docker compose` against that host (TCP+TLS or SSH). Host TLS certs are written
  to a private, mode-0600 temp dir only for the duration of the command and wiped
  afterwards. First cut supports **images and named volumes**: a compose file
  that bind-mounts a local host path is blocked on remote deploy with a clear
  message (the remote daemon can't see local files — file sync is a follow-up).
- **Private-registry tag autocomplete** — image-tag suggestions in the editor and
  Create-container form now also list tags from a **configured private registry**
  (the Docker Registry v2 API, with the registry's stored credentials and a Bearer
  token handshake), not just Docker Hub. Only hosts you've added under Registries
  are ever contacted; the token realm is constrained (https, no internal address)
  to avoid SSRF.
- **LDAP group → section mapping** — Settings → LDAP can now map LDAP groups to
  RBAC sections, so a user's allowed sections follow their group membership. A
  user's sections are the union across every mapping whose group they're in. When
  any mapping is configured, LDAP is authoritative for non-admin users' sections
  (recomputed from group membership on each login); group DNs match on the full
  DN case-insensitively and unknown section names are ignored. The admin role
  stays sticky once granted (no auto-demote on group removal).
- **Schema-aware Compose autocomplete** — editing a Compose file now suggests
  keys at the right nesting level (top-level, service, and nested `build` /
  `healthcheck` / `deploy` / `logging` blocks) and known enum values (e.g.
  `restart:` → `always` / `unless-stopped` / …) as you type, complementing the
  existing image-name completion and server-side `docker compose config`
  validation. Suggestions only apply to Compose files, not arbitrary YAML.
- **Built-in self-signed TLS certs** — `dockercmd --make-certs [hostnames…]`
  generates an ECDSA self-signed certificate + key into `<data-dir>/tls/` (key
  mode 0600), covering localhost / 127.0.0.1 / ::1 plus any hostnames or IPs you
  pass, and prints the `DC_TLS_CERT` / `DC_TLS_KEY` to serve HTTPS — no `openssl`
  needed. For public hosts use a real CA / Let's Encrypt.
- **Alert rule import/export** — the Alerts → Rules tab can now export every rule
  to a portable JSON bundle and import rules from one, so rule sets can be
  version-controlled or moved between instances. Webhooks are referenced by name
  (never by internal id, and their URLs/secrets are never included); on import an
  unknown webhook name leaves the rule without a destination and is reported.
  Imported rules are validated (type, severity, config shape and size) and always
  created anew — an import never overwrites or deletes existing rules.
- **In-app one-tap update** — admins now get an "Update & restart" button on the
  update-available banner. It downloads the latest release, verifies its SHA-256
  (the same fail-closed check as `--self-upgrade`), atomically replaces the binary
  and restarts the process in place (re-exec), then the UI reconnects on the new
  version. Gated to admins and disabled with `DC_SELF_UPDATE=0` (the banner still
  shows). Not offered on Windows (restart the service manually).

### Changed
- **Consistent tab bars** — Templates now uses the same underline tab strip as
  Alerts and the container detail view (a shared `Tabs` component), instead of
  its own pill buttons. Icons and per-tab counts are preserved.

### Security
- **Host TLS private keys are encrypted at rest** — a TCP host's client private
  key (`hosts.tls_key`) was stored in plaintext in the database; it's now
  encrypted with the same AES-256-GCM cipher that protects registry/SMTP secrets
  (the CA and client certificate are public, so they're stored as-is). Existing
  plaintext keys are migrated transparently on the next start.

### Fixed
- **Windows build** — `internal/tlscert` used `syscall.O_NOFOLLOW`, which is
  undefined on Windows and broke the cross-compiled Windows release binary. The
  flag is now applied via a platform constant (`O_NOFOLLOW` on Unix, no-op on
  Windows).

## [1.4.4] — 2026-06-15

### Fixed
- **The APT repository is now actually published.** In v1.4.3 the apt job failed
  to import the signing key (`gpg: no valid OpenPGP data found`) because the
  armored key in the `APT_GPG_PRIVATE_KEY` secret had lost its armor lines on
  paste. The key is now stored base64-encoded and decoded in CI, so a multi-line
  block can't be mangled — `apt install dockercmd` works from the signed repo.

## [1.4.3] — 2026-06-15

### Fixed
- **Release pipeline now actually publishes the `.deb`/`.rpm` packages and the
  APT repository.** In v1.4.2 these were lost: the packaging/apt jobs tried to
  add assets to an already-created **immutable** release and hit
  `422 Cannot upload assets to an immutable release`, which failed packaging and
  skipped the apt publish. The packages are now built in the `release` job and
  uploaded in the single `gh release create`, and `apt` publishes from the
  finished release. (Binaries, signatures, the container image and the Homebrew
  tap were unaffected in v1.4.2.)

## [1.4.2] — 2026-06-15

### Added
- **Homebrew tap** — install via `brew install koduj-dev/tap/dockercmd` (macOS &
  Linux/Linuxbrew). The formula pulls the signed release binary for your
  OS/arch; a release job renders it from `deploy/homebrew/dockercmd.rb.tmpl` and
  pushes it to [koduj-dev/homebrew-tap](https://github.com/koduj-dev/homebrew-tap)
  on each tag (when the `HOMEBREW_TAP_TOKEN` secret is set).
- **Debian/Ubuntu `.deb` and Fedora/RHEL `.rpm` packages** — built per release
  (amd64 + arm64) with `nfpm` and attached to the GitHub release. Each installs
  the binary, a hardened systemd unit, the man page and an
  `/etc/docker-commander/commander.conf` conffile, and sets up the `dockercmd`
  service.
- **Signed APT repository** — `apt install dockercmd` from a GPG-signed repo
  served on GitHub Pages (<https://koduj-dev.github.io/apt>). A release job adds
  the new `.deb` (after verifying its provenance) to the
  [koduj-dev/apt](https://github.com/koduj-dev/apt) archive with `reprepro` and
  re-signs it (gated on the `APT_REPO_TOKEN` + `APT_GPG_PRIVATE_KEY` secrets).

## [1.4.1] — 2026-06-15

### Added
- **`--help`, `--version`, and a `man` page** — `dockercmd --help` (and `-h`)
  now prints a complete usage: a synopsis, the **standalone actions**
  (`--version`, `--self-upgrade`, and `--install-service` /
  `--uninstall-service` / `--service-status` — previously undiscoverable because
  they're parsed before the flag set) and every option. `dockercmd --version`
  (or `dockercmd version`) prints the build version. Installing as a service now
  also installs a **`man dockercmd`** page — embedded in the binary, kept in
  sync with the flags by a test, and written by both `--install-service` and the
  `install-linux.sh` / `install-macos.sh` scripts.
- **Official container image** — a multi-arch (amd64/arm64) image published to
  **`ghcr.io/koduj-dev/docker-commander`** on each release. Built from a
  distroless base (no shell/package manager), it runs as a non-root user and
  embeds the UI; mount the Docker socket and a `/data` volume to run it. See the
  README Quick start.
- **Signed releases, SBOM & build provenance** — release artifacts now carry a
  keyless **cosign** signature over `SHA256SUMS` (`SHA256SUMS.sig` / `.pem`), an
  SPDX **SBOM** (`dockercmd.sbom.spdx.json`), and per-binary SLSA build
  **provenance** (`gh attestation verify …`); the container image gets provenance
  and SBOM attestations and is cosign-signed too. Verification steps are in the
  README.
- **`go install` support** — documented `go install
  github.com/koduj-dev/docker-commander/cmd/dockercmd@latest` as an install path.

### Security
- **Supply-chain hardening of the release pipeline** — every GitHub Actions step
  is now **pinned by commit SHA** (a moved tag can't slip code into a run that
  holds `id-token` / `attestations` / `packages` write); the cosign signature
  over `SHA256SUMS` also **covers the SBOM**; the container image is signed
  **recursively** (`cosign sign -r`, so each per-platform manifest is signed, not
  just the index); and the README's `docker run` is hardened (`--read-only`,
  `--cap-drop ALL`, `--security-opt no-new-privileges`, localhost bind, digest
  pinning) with an explicit warning that mounting the Docker socket grants
  host-root-equivalent access.

## [1.4.0] — 2026-06-15

### Security
- **Client IP is no longer spoofable** — the app previously used chi's
  `middleware.RealIP`, which trusts `X-Forwarded-For` / `X-Real-IP` from **any**
  client (chi now deprecates it as spoofable). Because every IP-based control —
  login & OAuth **rate limits**, the **loopback 2FA exemption**, and **audit**
  records — keys on the client address, a remote attacker could forge it: claim
  `127.0.0.1` to skip 2FA, or rotate `X-Forwarded-For` values to evade
  brute-force throttling. Forwarded headers are now honoured **only** from
  proxies you explicitly trust via **`DC_TRUSTED_PROXIES`** (IPs/CIDRs); with
  none set (the default) the real TCP peer is used and forwarded headers are
  ignored. The client IP is also normalised to a bare IP, fixing previously
  port-keyed (ineffective) rate limiting on direct connections. **If you run
  behind a reverse proxy, set `DC_TRUSTED_PROXIES`** so the real client IP is
  recovered — see [deployment](docs/deployment.md).

### Added
- **Configurable stats sampling interval** (`DC_METRICS_INTERVAL`, default `15s`)
  — the monitor samples every running container's stats on this interval to feed
  the charts/history and resource alert rules. On a host with **many containers**
  the sweep can dominate CPU (on the app and the Docker daemon); raising the
  interval is the first lever. Previously hard-coded.
- **Optional profiling endpoints** (`DC_PPROF=1`) — serves Go's `net/http/pprof`
  on a **dedicated `127.0.0.1:6060` listener** (separate from the main port, so
  it's physically unreachable off-box and immune to `X-Forwarded-For` spoofing),
  for diagnosing CPU/allocation/goroutine issues with `go tool pprof`. Off by
  default. See [deployment → diagnosing high CPU](docs/deployment.md).
- **Per-host reachability monitoring + alerts** — the engine now pings every
  enabled host on an interval and tracks whether its Docker daemon is reachable.
  The **Hosts** page and the sidebar **host switcher** show a 🔴 *unreachable*
  badge, and a host going **offline** (or **recovering**) raises an alert in the
  in-app feed and, when SMTP is configured, an email — honouring the host's
  per-host alert recipient. This watch is automatic; it needs no alert rule. The
  first probe never alerts, so an already-down host at startup stays quiet until
  it actually changes state.
- **Section-gated live stream** — the shared `/api/ws` WebSocket that streams
  container **stats** and **logs** is now checked against the user's **live
  RBAC** per subscription. It was previously open to any authenticated user;
  both channels now require the `containers` section (every consumer needs it to
  resolve a container anyway), so a user without it can no longer stream a
  container's data. Backed by an adversarial test.
- **MCP Admin overview** — a new admin-only page (**System → MCP Admin**) giving
  a fleet-wide view of MCP credentials: **every user's** active API tokens
  (annotated with the owner) and all registered **OAuth clients**, with the
  ability to **revoke** any token or **remove** any client (the latter purges its
  codes and refresh tokens too). Secrets are never exposed — only metadata. Gated
  by the `__admin` section; backed by store/handler unit tests and adversarial
  pen tests (admin-gating, IDOR, secret-leak). Makes MCP team-ready.
- **Remote control from AI tools (MCP)** — an optional, **off-by-default**
  **Model Context Protocol** server (`DC_MCP_ENABLED`) so AI tools (**Claude
  Code**, **Claude Desktop**, **Cursor**) can monitor and *safely* operate Docker
  **as the authenticated user**. ~20 tools — read (containers, logs, images,
  projects, volumes, networks, stats, metrics history, events, audit) and *safe*
  control (start/stop/restart a container, deploy/down a managed project) — plus
  MCP **resources** (container inventory, compose files) and **prompts** (curated
  ops workflows). Two auth paths: a **bearer API token** from a self-service
  **MCP Access** page (Claude Code / scripts), or a self-contained **OAuth 2.1**
  server — PKCE, dynamic client registration, audience-bound access tokens with
  rotating refresh tokens — for Claude Desktop / Cursor (needs
  `DC_MCP_PUBLIC_URL`). Every call reuses the app's **RBAC**, and a token can only
  **narrow** its owner's rights (a section subset + read-only). Container env
  vars, audit detail and event attributes are kept out of tool output; there is
  deliberately **no exec, image export, volume-content read, prune or remove**.
  Backed by unit, end-to-end runtime-smoke, and adversarial pen tests. Serve
  behind HTTPS. See [docs/mcp.md](docs/mcp.md).
- **Docker image autocomplete** — typing an image reference now suggests names
  and tags: in the **compose editor** (on `image:` lines) and in the **Create
  container** form. Suggestions blend the host's locally-pulled images (instant,
  offline) with a **Docker Hub** repository search (proxied through the host
  daemon, so no credentials leave the process) and, after a `:`, Docker Hub's
  **tag** list. Everything degrades to local-only when offline.
- **Builder — service instances & clusters** — a block can now be added **more
  than once**, each as a service **instance** with its own editable key (`db`,
  `db-2`, …) and its own named volumes (auto-de-duplicated), so you can build a
  cluster of the same service. The live preview now also **validates** the
  assembled compose (`docker compose config`) and shows valid / warnings /
  invalid.
- **Builder — shared definitions (YAML anchors)** — the builder can now include
  reusable **top-level anchors** (e.g. `x-pg-common: &pg-common …`) emitted above
  `services:`, so a cluster of services can share one definition (security, cert
  mounts, …). Pick which services **merge** each anchor (a `<<: *pg-common` is
  injected per instance) — including into otherwise read-only built-in services.
  Pick built-in anchors (Service defaults, Secured Postgres) or save your own;
  they're managed on the Templates page like service blocks.
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
  they're merged into one compose), and **Import** (`.zip`). **Save as preset**
  snapshots a project into a reusable preset, and you can add your own service
  blocks to the builder. Built-in presets/blocks are embedded; user-saved ones
  live in the data dir (the catalog is structured for a future remote source).
- **Self-install as a service** — `dockercmd --install-service` sets the binary
  up as a **systemd** service (Linux) or a per-user **launchd** LaunchAgent
  (macOS), with `--uninstall-service` and `--service-status`. Equivalent
  idempotent installer scripts also ship in `deploy/` (`install-linux.sh`,
  `install-macos.sh`, and `install-windows.ps1` via a Scheduled Task).

### Changed
- **Sidebar navigation** — the *Compute* group is split into **Workloads**
  (Containers, Stacks, Projects, Templates) and **Storage** (Images, Volumes) so
  the menu stays scannable as the feature set grows.

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

[1.5.1]: https://github.com/koduj-dev/docker-commander/releases/tag/v1.5.1
[1.5.0]: https://github.com/koduj-dev/docker-commander/releases/tag/v1.5.0
[1.4.4]: https://github.com/koduj-dev/docker-commander/releases/tag/v1.4.4
[1.4.3]: https://github.com/koduj-dev/docker-commander/releases/tag/v1.4.3
[1.4.2]: https://github.com/koduj-dev/docker-commander/releases/tag/v1.4.2
[1.4.1]: https://github.com/koduj-dev/docker-commander/releases/tag/v1.4.1
[1.4.0]: https://github.com/koduj-dev/docker-commander/releases/tag/v1.4.0
[1.3.0]: https://github.com/koduj-dev/docker-commander/releases/tag/v1.3.0
[1.2.0]: https://github.com/koduj-dev/docker-commander/releases/tag/v1.2.0
[1.1.0]: https://github.com/koduj-dev/docker-commander/releases/tag/v1.1.0
[1.0.0]: https://github.com/koduj-dev/docker-commander/releases/tag/v1.0.0
