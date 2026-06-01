# Where we continue — Docker Commander working notes

_Last updated: 2026-06-01._

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

Everything above is committed & pushed to `origin/main`
(`git@github.com:koduj-dev/docker-commander.git`) and verified end-to-end
against the local `red2_*` stack (headless Chrome + Go/WS probes).

## ▶️ Next up (priority order)

1. **SSH securer — known_hosts verification (BLOCKER for external use).**
   `internal/docker/ssh.go` currently uses `ssh.InsecureIgnoreHostKey()`.
   Replace with `knownhosts` (golang.org/x/crypto/ssh/knownhosts) reading
   `~/.ssh/known_hosts`; on unknown host, surface a clear "host key not trusted"
   error (and maybe a UI affordance to pin/accept). Do NOT ship remote/SSH to
   untrusted networks until this is in. (User: "bez toho to nechci ani pouštět ven.")

2. **Images management.** List local images (`ImageList`), show size/tags/
   created, pull (`ImagePull` with progress over WS), remove (`ImageRemove`,
   handle in-use), prune dangling. New page + API.

3. **Volumes management + inspector.** List volumes (`VolumeList`), inspect
   (driver, mountpoint, labels, scope), show which containers use a volume,
   create/remove, prune.

4. **Data transfer (docker cp).** Download from container
   (`CopyFromContainer` → tar stream → browser download) and upload
   (`CopyToContainer` from an uploaded tar/file). Wire into the container detail
   (a "Files" tab or buttons) and/or volume inspector.

5. **Email/SMTP alert channel** (alongside webhooks + Prometheus).

6. **Structured log views & saved parsing rules** (named regex field
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
- Real test data: the running `red2_*` compose stack (nginx/symfony/db/nodejs).

## 🔧 Config quick reference (env / flags)

`DC_ADDR`, `DC_DATA_DIR`, `DC_DEV`, `DC_METRICS_TOKEN`, `DC_REDIS_ADDR`,
`DC_REDIS_PASSWORD`, `DC_REDIS_DB`, `DC_METRICS_RETENTION`, `-session-ttl`.
SSH hosts authenticate with the server's ssh-agent / `~/.ssh` keys.
