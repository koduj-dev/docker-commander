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

## 🧭 "Control everything" plan (decided 2026-06-02 with a colleague)

Goal: expose the rest of the Docker Engine API. Agreed order: **A done** (above),
then **E next** (registry/build — wanted "hned teď"), then B, C, D.

- **E. Registry & build (NEXT).** push + private pull (store registry creds
  **encrypted** in DB), `docker build` with build-context upload (stream
  progress over WS like pull). User accepts the security overhead.
- **B. Container lifecycle.** create/run (form), rename, update (CPU/MEM limits),
  commit (container→image), unpause toggle, restart-policy edit.
- **C. Image transfer & tags.** tag, save (export tar download), load (upload
  tar), import, container export (FS as tar).
- **D. Volumes + networks full CRUD** (see item 1 below for volumes).

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
