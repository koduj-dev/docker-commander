# Where we continue — Docker Commander working notes

_Last updated: 2026-06-02._

This is the "pick up here tomorrow" file. The polished feature list lives in
[README.md](./README.md#-features); this file tracks **status + next steps +
dev notes** so we don't lose context.

## ✅ Done so far

- **Phase 1 (MVP):** Go single-binary backend with embedded React SPA; auth
  (Argon2id password + mandatory TOTP 2FA, JWT cookie, login rate-limit);
  container list/inspect/lifecycle; live logs + RT CPU/RAM over WebSocket;
  networks; audit log; SQLite store; cross-compiles for linux/macOS/windows.
- **Phase 2:** network topology graph (React Flow + dagre, draggable); global
  multi-container log aggregation (level detection, search, pause, persisted
  selection, **chronological interleaving**); alerting engine (state / resource
  / log-pattern / restart triggers) → in-app feed + generic webhooks +
  Prometheus `/metrics`.
- **Phase 3:** interactive **console** (exec TTY over WS, xterm.js, host-
  agnostic); **metrics history** (Redis via `DC_REDIS_ADDR`, else in-memory) +
  historical charts; **multi-host** (CRUD + sidebar switcher, `?host=` threaded
  through REST/WS/exec, TCP+TLS and SSH).
- **SSH host-key verification (the securer; 2026-06-02):** replaced
  `InsecureIgnoreHostKey` with a real trust policy in
  `internal/docker/ssh.go` → `verifyHostKey`: pinned key in DB (new `host_key`
  column) wins; else `~/.ssh/known_hosts`; else **unknown** → the Hosts page
  shows the SHA256 fingerprint + a "Trust this host" button (TOFU, key captured
  server-side via `ProbeHostKey` and pinned with `POST /api/hosts/{id}/trust`).
  A **changed** key is a hard `HostKeyMismatchError` (possible MITM) requiring
  explicit re-trust. Verified by unit tests (`ssh_test.go`) + an env-gated
  integration test against a live sshd (`ssh_integration_test.go`,
  `DC_SSH_INTEGRATION=user@host:port`).

Everything above is committed & pushed to `origin/main`
(`git@github.com:koduj-dev/docker-commander.git`) and verified end-to-end
against the local `red2_*` stack (headless Chrome + Go/WS probes).

## ✅ Done so far (continued)

- **Images management (2026-06-02).** `internal/docker/images.go` +
  `internal/api/image_handlers.go` + `web/src/pages/Images.tsx`. List with
  size/tags/age, **in-use** flag (cross-referenced against existing containers'
  ImageIDs) and **dangling** flag; **pull** with live per-layer progress over a
  WebSocket (`GET /api/images/pull?ref=…`, mirrors the exec bridge); **remove**
  (`DELETE /api/images?ref=…&force=1`) with a force fallback when in use; **prune**
  dangling (`POST /api/images/prune`). New "Images" nav item + route. Verified
  end-to-end against real Docker (pull→list→remove→inUse) with a Node `ws`
  harness. NOTE: `ref` is a **query param**, not a path segment — image refs
  contain `:`/`/` which chi does not decode cleanly in `{id}`.

- **Inspect & observe — batch A of "control everything" (2026-06-02).**
  `internal/docker/observe.go` + handlers in `docker_handlers.go` /
  `events_handler.go` + `web/src/components/InspectModal.tsx` + Events page.
  Generic raw **inspect** (`GET /api/inspect/{kind}?id=…` for
  container/image/network/volume — id is a query param), container **diff**
  (`/containers/{id}/diff`) and **top** (`/containers/{id}/top`) as detail tabs,
  image **history** (`/api/images/history?ref=…`) modal, live **events** feed
  (WS `GET /api/events`) as a new nav page, and **disk usage**
  (`GET /api/system/df`) cards on the dashboard. Verified end-to-end: backend
  via a Node API harness, UI via a puppeteer smoke test (TOTP bypass).

- **Topology readability + fullscreen (2026-06-02).** A 50-container bipartite
  graph was shrinking to ~23% under fitView; now `fitViewOptions.minZoom: 0.5`
  floors the fit so nodes stay readable (pan / minimap / zoom out to 0.15 for
  the full picture), and `ranksep` widened to use more horizontal space.
  Fullscreen now actually fills the screen — the pane's inline `calc()` height
  was overriding the browser's fullscreen sizing; a `.dc-topo:fullscreen`
  `!important` rule fixes it, and the graph refits on fullscreenchange.

- **Polish round (2026-06-02).** Sidebar nav grouped into sections
  (Compute / Network / Observability / System). Reusable
  `components/ListControls.tsx` (search + 10/20/50/100 pagination) applied to
  Containers and Images. Networks: internal/**external** badge, raw inspect,
  **remove** (`DELETE /api/networks/{id}`; predefined bridge/host/none guarded,
  daemon errors surfaced), and the topology modal's centre node made opaque so
  edges no longer show through. Topology: **floating edges**
  (`components/FloatingEdge.tsx`, anchor to node boundary — fixes the drag
  "spider" tangle), filter toggles (hide empty networks / show stopped),
  **stopped containers now included** (topology links built from each
  container's NetworkSettings, not the network's active-endpoint list), dark-
  themed zoom controls + a working **fullscreen** button. Logs: **regex**
  search toggle. Events: full-height flex layout. All verified via puppeteer.

- **Registry & build — batch E (DONE 2026-06-02).** Encrypted credential store
  (`internal/crypto` AES-256-GCM, key persisted in settings; `store/registries.go`
  with secrets sealed at rest, never returned by the API). `internal/docker/
  registry.go` resolves auth by an image ref's registry host (Docker Hub aliases
  normalised) and adds **push** (WS), **tag**, and a daemon **login** test;
  PullImage now attaches stored auth for **private pulls**. `internal/docker/
  build.go` builds from an uploaded tar context and streams output; the build
  handler streams NDJSON over a plain POST (`/api/images/build`, tar body).
  Frontend: **Registries** page (System nav), Push & Build modals on Images.
  Verified end-to-end against a local **authenticated** registry (registry:2 +
  htpasswd): create→login→tag→push→remove→private-pull→build, secret confirmed
  encrypted in the DB; UI verified with puppeteer.

## 🧭 "Control everything" plan (decided 2026-06-02 with a colleague)

Goal: expose the rest of the Docker Engine API. **All of A–E DONE (2026-06-02).**

- **A. Inspect & observe** — done (see above).
- **B. Container lifecycle** — done: create/run (`docker/lifecycle.go` +
  `CreateContainerModal`), rename, update (mem/CPU limits — set MemorySwap=mem to
  satisfy the daemon), commit, restart-policy. Routes: static
  rename/update/commit registered before the `{action}` catch-all.
- **C. Image transfer** — done: save/load/import + container export
  (`docker/transfer.go`, `LoadModal`, download via `<a>`/triggerDownload).
- **D. Volumes** — done: list/inspect/create/remove/prune + in-use detection
  (`docker/volumes.go`, Volumes page). Networks delete was done in the polish round.
- **E. Registry & build** — done (see above).

- **File browser (`docker cp`) — DONE 2026-06-02.** `internal/docker/files.go`
  (`ls`/`rm` via one-shot exec capture using stdcopy; CopyFrom/CopyTo for
  download/upload) + `files_handlers.go` + `components/FileBrowser.tsx`. The
  container detail's old "Files" tab (which was `docker diff`) was renamed
  **Changes**; the new **Files** tab is a real browser: navigate dirs, download
  a file or a directory (tar), upload into the current dir, delete paths.
  Verified backend + UI against a live alpine container.

- **Email/SMTP alert channel — DONE 2026-06-02.** Encrypted SMTP config
  (`store/smtp.go`), `monitor/email.go` (STARTTLS/implicit TLS), per-rule email
  flag, Email tab + test. Verified via mailpit.
- **Structured log parsing — DONE 2026-06-02.** Saved parse rules (regex with
  `(?<name>…)` groups; `store/parse_rules.go`), applied client-side
  (`lib/parse.ts`) to render a column view on the Logs page + a manage modal
  with live preview. Verified via puppeteer (INFO/path/status → columns).

- **Multi-user + RBAC + feature flags + localhost-2FA (DONE 2026-06-02, company
  request).** `users` gained `role`/`read_only`/`sections`; admins manage
  accounts (`/api/users`) and app settings (`/api/settings`: globally disabled
  sections + localhost-2FA toggle). Enforcement: `internal/api/access_middleware.go`
  maps each path to a section and checks role / section grant / read-only /
  global-disable after RequireSession. `/api/auth/me` returns the effective
  sections + `mfaEnforced`; the frontend hides nav and the 2FA enrollment gate
  accordingly. Login takes `exemptMFA` (loopback + setting) to skip 2FA. New
  Users + Settings admin pages. Verified backend (RBAC matrix) + UI (puppeteer).
  KNOWN LIMIT: the shared stats/logs WebSocket (`/api/ws`) is not section-gated
  (it's a multiplexed read stream); section RBAC is enforced on REST endpoints.

- **The five company follow-ups — ALL DONE (2026-06-02):**
  1. systemd unit + deploy docs (`deploy/`).
  2. log parsing presets (`web/src/lib/parsePresets.ts`).
  3. alert rule editing (`UpdateAlertRule` + PUT + reusable form).
  4. multi-host monitoring + per-host email (monitor watches all hosts; alert
     events carry host; `hosts.alert_email` overrides the global recipient).
  5. **LDAP** (`internal/store/ldap.go`, `internal/auth/ldap.go`): local-first
     login, else LDAP bind + provision a local account (auth_source="ldap",
     admin-group→admin role); config in Settings, bind password encrypted.
     Verified end-to-end against osixia/openldap.

The roadmap is fully complete. Future ideas only: LDAP group→section mapping,
SSO/OIDC, alert rule import/export.

## ⚠️ Gotcha worth remembering

`internal/api/respond.go` `decodeJSON` sets `DisallowUnknownFields()`, so request
bodies must contain ONLY fields the Go struct declares. The SMTP UI initially
sent the read-only `hasPassword` field back on save and got a silent 400 — strip
derived/read-only fields before PUT/POST (see `smtpPayload` in `web/src/lib/api.ts`).

## ▶️ Next up (priority order)

1. **Volumes management + inspector.** List volumes (`VolumeList`), inspect
   (driver, mountpoint, labels, scope), show which containers use a volume,
   create/remove, prune.

2. **Data transfer (docker cp).** Download from container
   (`CopyFromContainer` → tar stream → browser download) and upload
   (`CopyToContainer` from an uploaded tar/file). Wire into the container detail
   (a "Files" tab or buttons) and/or volume inspector.

3. **Email/SMTP alert channel** (alongside webhooks + Prometheus).

4. **Structured log views & saved parsing rules** (named regex field
   extraction, column view).

## 🛠️ Dev / test notes (this machine)

- **Go** is at `~/.local/go` (1.26.3) — NOT on default PATH. Prefix bash:
  `export PATH="$HOME/.local/go/bin:$PATH"`. `GOTOOLCHAIN=local` is set.
- **Do not use `pkill`** in this sandbox — it exits 144 and aborts the rest of
  the command. Run servers via background tasks and stop them explicitly.
- **Build:** `make build` (UI then binary) or, for backend only,
  `go build ./...`. The committed `web/dist` lets `go build` work without Node.
- **Headless test harness:** `/tmp/pptr/` has puppeteer-core driving the
  system `google-chrome`, plus a Node TOTP helper to pass mandatory 2FA
  (`enroll.js` does first-run setup + prints `DC_TOTP_SECRET`; drivers log in
  with it). Pattern: fresh data dir + port per run, `node enroll.js`, then the
  driver. This is how the logs live-tail, interleaving, network modal, topology
  drag, and host-switch plumbing were all verified.
- **Local Redis for metrics history test:**
  `docker run -d --rm --name dc-redis-test -p 6399:6379 redis:7-alpine`, then
  run the server with `DC_REDIS_ADDR=127.0.0.1:6399`.
- **Local authenticated registry for push/pull test:** no `htpasswd` on this
  box, so generate the bcrypt entry with Go (`golang.org/x/crypto/bcrypt`,
  already a dep) into `auth/htpasswd`, then
  `docker run -d --name dc-reg -p 5999:5000 -v <auth>:/auth -e REGISTRY_AUTH=htpasswd
  -e REGISTRY_AUTH_HTPASSWD_REALM=... -e REGISTRY_AUTH_HTPASSWD_PATH=/auth/htpasswd registry:2`.
  localhost is insecure-by-default for the daemon, so HTTP + basic auth works.
- **Local sshd for host-key test:** `docker run -d --name dc-sshtest -p 2222:22
  alpine:latest sh -c "apk add --no-cache openssh && ssh-keygen -A && adduser -D
  test && echo 'test:test' | chpasswd && /usr/sbin/sshd -D -e"`, then
  `DC_SSH_INTEGRATION=test@127.0.0.1:2222 go test ./internal/docker/ -run
  TestSSHHostKeyEndToEnd -v`. (Host key is exchanged before auth, so it passes
  even though this sshd has no docker daemon.)
- Real test data: the running `red2_*` compose stack (nginx/symfony/db/nodejs).

## 🔧 Config quick reference (env / flags)

`DC_ADDR`, `DC_DATA_DIR`, `DC_DEV`, `DC_METRICS_TOKEN`, `DC_REDIS_ADDR`,
`DC_REDIS_PASSWORD`, `DC_REDIS_DB`, `DC_METRICS_RETENTION`, `-session-ttl`.
SSH hosts authenticate with the server's ssh-agent / `~/.ssh` keys.
