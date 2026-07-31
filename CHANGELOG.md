# Changelog

All notable changes to Docker Commander are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/), and the project uses
[semantic versioning](https://semver.org/).

## [Unreleased]

### Added
- **The alert feed is paged, filterable, and says whether alerts were actually
  delivered.** It previously rendered every event on one page with no filters, which
  stops being usable at the point alerting starts being useful. It now pages 50 at a
  time and filters by severity, lifecycle kind, rule, container and message text,
  with an *unacknowledged only* toggle — **in the database**, so the totals and paging
  describe the whole result set rather than the page in front of you. Host scoping
  moved into the query for the same reason: filtering a page after fetching it yields
  short pages and a total that counts events the viewer isn't allowed to see.

  **Every webhook call and e-mail send is now recorded against its alert** with the
  outcome, HTTP status and an excerpt of the response. A webhook returning 500, an
  SMTP server refusing the connection, or a rule with *e-mail* ticked while no
  recipient is configured anywhere all used to fail silently — the alert appeared in
  the feed and looked handled while nothing had left the building. The webhook's
  **name and host** are stored rather than its URL, because those routinely carry a
  token and this record is readable by anyone with the alerts section; response text
  is truncated so a remote endpoint can't write unbounded data into the database.
  There is no automatic retry yet: failures are recorded, not re-attempted.

  **Acknowledging an alert records who did it** and when, and **Ack all** clears
  everything matching the current filters behind a confirm that names which of the
  two it will do. Filters include the **host**.

  **A toast appears when an alert arrives while the app is open**, with a countdown
  bar that pauses on hover, and can be turned off per account under
  *Profile → Preferences*. The feed, the sidebar badge and the toasts share a single
  poll: they had separate timers at first, so a row could appear in the table
  seconds before the toast announcing it.

### Changed
- **Threshold alerts are conditions with a lifetime, not lines reprinted every
  minute.** Testing the engine against a real stack produced an alert log that was
  the same seven lines repeated every 60 seconds, and three separate problems were
  behind it. Every `resource` rule was evaluated independently, so a container over
  both a *warning >5%* and a *critical >10%* memory rule emitted **two** alerts for
  one fact. Nothing tracked whether a condition was already known, so the cooldown
  expiring re-announced it as if it were new. And nothing was ever emitted when a
  problem went away, so the log could not tell you whether it was still happening.

  A threshold alert is now one **condition per container + metric**, regardless of
  how many rules notice it, and only the most severe rule speaks for it. You get an
  event when it starts, when it **escalates** or **eases** to a different severity,
  when the re-notify interval elapses (`repeat`), and when it **resolves** —
  carrying how long it lasted. Escalation updates the existing condition rather
  than opening a second one, so the incident clock keeps running. Silence now means
  "unchanged", which is the whole point.

  Conditions are stored, so they survive a restart: without that, nothing would
  ever resolve across one. `state`, `log` and `restart` rules stay edge-triggered
  and keep the plain cooldown — a container that died has no later moment at which
  it stops having died.

- **CPU thresholds can be a percentage of the whole machine.** Docker's CPU figure
  is per-core — 100% is one core — so a container busy on four cores reads ~400%
  and a `> 80%` rule fires permanently on any multi-core host. Rules gained a
  **CPU % (of all cores)** metric that normalises by the core count, the editor now
  says which basis each option uses, and existing rules keep the per-core meaning
  they were written with.

- **Alert messages say what the number measures.** `MEM 61.9% > 5%` never revealed
  whether that was of host RAM or of the container's limit (it is the limit). It
  now reads `MEM 3.0 GB / 5.0 GB (61.9% of limit) > 5%`, and CPU messages name
  their basis and the core count.

### Security
- **Removing an MCP OAuth client now revokes its access immediately.** Deleting a
  client purged its authorization codes and refresh tokens, which stopped it
  obtaining a *new* access token — but an access token is a **signed** credential,
  so nothing about deleting a database row reached the copy a connected tool
  already held. It kept working until it expired: up to 15 minutes of access after
  an admin pressed Revoke. Access tokens are now bound to the client that was
  issued them (a `dc_cid` claim) and every call requires that client to still be
  registered. The window was always bounded, so this is hardening rather than a
  hole — but "revoked" should mean now, and the admin dialog said so while the
  behaviour didn't. Tokens minted before this carry no binding and simply expire
  within one lifetime; rejecting them would have forced every connector to
  re-authorize on upgrade to buy at most fifteen minutes.

- **The stack editor's backup could be redirected through a symlink.** Saving a
  CLI-discovered stack's compose file keeps the previous version beside it as
  `<name>.dc-prev`, and that backup was written with a plain write — which follows
  a symlink already sitting at the destination. Anyone able to create files in the
  stack's directory (a deploy or CI account, *not* necessarily one with Docker
  access) could pre-place `compose.yml.dc-prev` as a link to, say, `/etc/cron.d/`
  and have Docker Commander — commonly root — write through it, with content they
  also controlled, since the backup is the previous compose file. Backups are now
  written with a temporary file plus a rename, which replaces a symlink instead of
  following it; the SSH path does the same with `mktemp`, where `cat >` and `cp`
  had the same weakness. Found in a review of the release, before any tagged build
  shipped it.
- **Redeploying a stack skipped the containment check that saving enforced.** A
  compose path outside the stack's working directory was refused for writes but
  still handed to `docker compose up -d`, which would deploy whatever definition
  sat there. Both operations now answer the same question in one place, which also
  means the UI stops offering an editor whose Save could only ever fail. Exploiting
  it required direct Docker API access (to set the labels), which is already
  root-equivalent — so this hardens a boundary rather than closing a path into the
  app.

### Fixed
- **A role scoped to only-invalid hosts became unscoped instead of being refused.**
  Sending `hostIds: [0]` was sanitised down to an empty list, which means *every
  host* — so a request to narrow a role quietly produced an unrestricted one, with
  an audit line describing a scope that wasn't there. An explicit scope that
  survives no validation is now a 400, matching the rule already applied to MCP
  token section scopes.
- **A non-positive `?host=` is the local daemon everywhere.** The Docker layer has
  always treated `hostID <= 0` that way, but the new host-scope check took it
  literally: `?host=-1` was served by the local daemon while being authorised and
  audited as host −1, so a scoped user was refused something they were allowed.
  Normalised at all three entry points — REST, the WebSocket subscribe frame and
  MCP tool arguments.
- **Deleting the LDAP fallback role could slip through** when the LDAP settings
  could not be read (corrupt JSON, transient DB error): the guard was skipped
  rather than failing closed, which is the one situation it exists for.
- **The profile page span forever if loading permissions failed** — "still loading"
  and "failed" were the same state. It now says what went wrong and offers a retry.

### Fixed
- **Redeploying a stack rebuilds its image too.** The same staleness bug as the
  project deploy below, on the other code path: `StackRedeploy` ran a plain
  `docker compose up -d`, so a CLI-discovered stack declaring `build:` kept running
  the image from its first deploy however much its Dockerfile or context changed on
  the host. Found while auditing the docs against the merged code, right after the
  project half was fixed — the two deploy paths are separate code and only one of
  them had been corrected.

- **Deploying a project no longer runs a stale image.** A project with a `build:`
  section was built on its first deploy and then never again: `docker compose up -d`
  builds a service only when its image is **missing**, so every later deploy reused
  the original image no matter what changed in the Dockerfile or its build context.
  Edit a Dockerfile in the project editor, hit Deploy, and the CLI reported
  `Container Running` — a wrong answer delivered as a success. Deploy now passes
  `--build`, which is a no-op for services that only pull an image, so an image-only
  project pays nothing. `POST` a deploy with `{"build": false}` to opt out. The MCP
  `deploy` tool matches, so the same project behaves the same way from either.

  While confirming this, one roadmap item turned out to rest on a wrong premise:
  **uploading `build:` contexts to a remote host already worked.** Docker's build
  API takes the context as a tar from the client, so a remote daemon receives the
  local folder and builds the image there — unlike a bind mount, which nothing
  uploads and which the app therefore seeds into a volume. Both are now covered
  together by an end-to-end test against a second daemon.

### Added
- **Edit and redeploy a stack that wasn't created here.** Stacks started with the
  host's `docker compose` CLI have always been visible and their compose file
  readable; now it can be changed. The viewer became an editor with **Save** (write
  the file back to the host) and **Redeploy** (`docker compose up -d`) as separate
  steps, so a half-finished edit doesn't restart anything.

  **The file is edited in place, not adopted into a managed project.** That is the
  whole design rather than an implementation detail: relative paths in a compose
  file — bind mounts, `env_file`, `build.context`, `include` — resolve against the
  project's working directory, so copying the file elsewhere silently repoints
  every one of them. `./nginx.conf` would stop meaning the operator's config and
  start meaning whatever sits beside the copy, which is usually nothing — and a
  missing bind source deploys as an *empty* file instead of failing. Redeploy
  therefore runs in the stack's original working directory. For SSH hosts that
  means compose runs **on the host**, so the host needs the plugin; plain-TCP hosts
  expose no filesystem and stay read-only, and the editor says so instead of
  silently not appearing.

  Guarded, because the compose path comes from a container **label** and labels are
  set by whoever started the container: a write must land **inside the stack's own
  working directory** (symlink-resolved locally) and on a `.yml`/`.yaml` path, or
  it is refused — otherwise a `containers` grant would have become an arbitrary
  file write as the account the app runs under. The replacement is validated with
  `docker compose config` **before** anything is replaced, the previous version is
  kept as `<name>.dc-prev`, the swap is a rename so an interruption can't leave a
  half-file, and the original file's permissions are preserved. `--remove-orphans`
  is deliberately not passed, so deleting a service never silently deletes a
  container.

- **A Docker version compatibility matrix, and a README that says which versions
  are tested.** The app talks to Docker two ways — the Engine API through the Go
  SDK and the `docker compose` CLI as a subprocess — and both move, while users run
  whatever their distro ships. Until now nothing said which versions that covers.

  A [nightly workflow](.github/workflows/compat.yml) re-runs the whole Docker
  integration suite against a pinned `docker:NN-dind` for **Engine 24 through 28**
  and reports the negotiated API version. It isn't a new set of tests: pointing
  `DC_COMPAT_DOCKER` at a daemon swaps the fixture's host, so every existing test
  follows along. The README now states the floor — **Engine API 1.43 (Docker 24)** —
  and a test fails if the app negotiates below it, so the table can't quietly drift
  from reality.

  All five majors pass: Engine 24.0.9 (API 1.43), 25.0.5 (1.44), 26.1.4 (1.45),
  27.5.1 (1.47) and 28.5.2 (1.51). The matrix pins the **Engine**; the Compose
  plugin is whichever one the runner ships, so it answers "which daemons work",
  not "which Compose releases work".

  **It caught a real difference on its first run.** On Engine 24, `/containers/json`
  reports a container as `running` for ~250 ms after it has stopped, while `inspect`
  already says `exited`; newer engines are consistent immediately. The app polls, so
  a user sees the right state on the next refresh — but the stack test asserted
  instantaneous consistency and was the one failure in the Engine 24 run. It now
  polls with a bounded wait.

- **Moving a project to another host can now actually move it.** Changing a
  deployed project's target host left the stack running on the old one — the app
  kept no record that anything was there, so you ended up with two live copies and
  only one of them visible as "the project's host". The settings dialog now offers
  to bring it down on the host it is leaving, ticked by default, with a danger
  confirm that says exactly what goes: containers stopped and removed, and the
  volumes seeded there for its bind mounts deleted. **Named volumes holding your
  data are left alone.** Unticking it keeps the old behaviour, and the dialog says
  what that means too.

  The teardown runs **before** the record moves, and a failure aborts the change —
  otherwise a stack would be left running on a host the app no longer points at,
  which is the same problem made invisible. The old host needs the same permission
  as the new one, so "move it away" can't become a way to stop workloads on a host
  you were scoped away from.

- **Per-host RBAC scoping** — a role can be limited to specific Docker hosts, so
  *"may restart containers"* can mean *"on staging, not production"*. This is
  phase 2 of [design/rbac-roles-and-host-scoping.md](design/rbac-roles-and-host-scoping.md).

  **This is new authorization, not a tightened check.** Until now a non-admin
  holding a section could act on **any** daemon by passing `?host=N` — including
  hosts they couldn't see on the Hosts page, which is gated by the separate
  `hosts` section. The check now lives in the permissions middleware that every
  host-targeting route already passes through, so all ~60 call sites are covered
  at once and a route added later is covered without anyone remembering.

  It is enforced on **every** surface a host can be named on: REST (`?host=`), the
  **WebSocket** subscribe frame, **MCP** tools' `host_id`, and a managed project's
  own target host (which lives in the project record, not the URL). MCP tokens can
  now be narrowed to a host subset too, and the audit log records which host an
  action happened on.

  Nothing changes on upgrade: **an empty host list means every host**, so every
  existing role and account keeps exactly its current reach, and a section granted
  directly on an account stays unscoped. The **local daemon is always in scope** —
  making it scopeable would let a single-host install lock itself out.

  **Scoping hides as well as blocks** (phase 3, landed in the same release). A host
  outside your scope is absent from the host list, its projects aren't listed, its
  alerts don't reach the feed *or the unread badge*, its entries don't appear in the
  audit log, and a container's metrics history is refused even if you know the
  container id.

  Three dashboard endpoints — `/api/stats/overview`, `/api/system/df` and
  `/api/stats/ports` — took `?host=` while belonging to **no section**, so the check
  above returned before it ever looked at the host and they served another host's
  counts, disk usage and published ports. Ungated means "no section required", not
  "any host you like"; a named host must now be one your grants reach.

  The metrics series is keyed by container id alone, so knowing an id used to be
  enough to read its CPU/memory history from any host. The monitor already knew
  which host each sample came from and was discarding it; it's now recorded and the
  series is authorized against it. An id with nothing recorded is treated as
  **unknown**, not as the local daemon, so a scoped caller can't probe ids to learn
  what exists.

  > The **alert engine** still watches every host, by design — it's background work
  > with no user context. A rule that lists you as a recipient can therefore mail you
  > about a host you can't see in the app. That's how you configure recipients, not
  > something the app decides per viewer.

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
