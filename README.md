# 🐳 Docker Commander

A self-hosted, open-source **Docker monitoring & control panel** with an
enterprise-grade UI. Monitor containers in real time, control their lifecycle,
stream and filter logs, watch live resource graphs, and inspect networks — all
from a single binary you download, build, and run.

> One Go binary with the web UI embedded. No external database, no runtime
> dependencies, CGO-free. Runs on **Linux, macOS, and Windows**.

---

## ✨ Features

- **Real-time monitoring** — live CPU / memory graphs streamed over WebSockets.
- **Container control** — **create/run** (image, ports, env, volumes, limits,
  restart policy), start, stop, restart, pause, kill, **rename**, **update**
  limits & restart policy at runtime, and **commit** to a new image.
- **Image management** — list local images with size / tags / age, flag which
  are in use or dangling, **pull** with live per-layer progress (over a
  WebSocket), remove (with a force fallback), and prune dangling images;
  per-image **history** (build layers) and raw **inspect**.
- **Registries & build** — store registry credentials (**encrypted at rest**),
  pull **private** images and **push** with live progress, **tag** images, and
  **build** from an uploaded tar context with streamed build output.
- **Transfer** — **save** images to a tar download, **load** a docker-save
  archive, **import** a filesystem tarball as an image, and **export** a
  container's filesystem as a tar.
- **Inspect & observe everything** — raw JSON **inspect** for containers,
  images, networks and volumes; container **diff** (filesystem changes) and
  **top** (live process list); a live **events** feed streaming every daemon
  event over a WebSocket; and a **disk usage** (`system df`) breakdown on the
  dashboard.
- **Interactive console** — a real shell (xterm.js) into any running container,
  streamed over a WebSocket; works the same on local and remote hosts.
- **File browser** — browse a container's filesystem, **download** files or a
  directory (as a tar), **upload** files into it, and delete paths (`docker cp`).
- **Multi-host** — manage local, **TCP(+TLS)** and **SSH** Docker hosts and
  switch between them; every view and stream rebinds to the selected host. SSH
  daemon **host keys are verified** (`~/.ssh/known_hosts` or a key you trust on
  first connect); a changed key is refused as a possible MITM.
- **Live logs** — follow `stdout`/`stderr`, filter by substring, toggle streams.
- **Aggregated logs** — a global Logs view streaming many containers at once,
  color-coded by source with automatic log-level detection, level filtering,
  and **regex search**. Save **parsing rules** (regex with named groups) to
  view logs as **structured columns**.
- **Container detail** — image, command, env vars, mounts, ports, networks,
  health, restart policy.
- **Volumes** — list with driver/scope/mountpoint, see **which containers use
  each volume**, raw inspect, create, remove (force fallback), and prune unused.
- **Networks & topology** — networks with drivers/subnets/scope and an
  internal/external flag, raw inspect and removal of user-defined networks,
  plus an interactive **connectivity graph** (containers ↔ networks) you can
  pan/zoom/fullscreen, with floating edges, filters (hide empty networks, show
  stopped containers) and stopped containers included.
- **Search & paginate** — Containers and Images have client-side search and
  page-size controls (10/20/50/100); Logs supports **regex** search.
- **Alerting** — user-defined rules on container **state changes**, **resource
  thresholds** (CPU/MEM), **log patterns** (substring/regex), and **restart /
  crash loops**, with per-rule severity and cooldown, editable in place. The
  engine watches **all configured hosts**, and email alerts can be routed
  **per host** (each host can override the global recipient).
- **Notifications & export** — fire alerts to **generic webhooks** (Slack,
  Discord, Grafana alerting, n8n…) with Go-template payloads, **email** via your
  own SMTP server (password encrypted at rest), an **in-app alert feed**, and a
  **Prometheus `/metrics`** exporter for Grafana dashboards.
- **Audit log** — every privileged action is recorded (who, what, when, from where).
- **Users & roles** — multiple accounts with **per-section permissions** and a
  **read-only** mode; admins manage users and can **disable whole sections**
  app-wide (feature flags). Optional **LDAP / Active Directory** login with
  auto-provisioning and admin-group mapping.
- **Security first** — password login with **Argon2id**, **TOTP 2FA**
  (optionally exempt for localhost), signed session cookies, login rate
  limiting, strict security headers, and **verified SSH host keys** for remote
  hosts. Registry/SMTP secrets are encrypted at rest.

## 🏗️ Architecture

```
React + TypeScript SPA  ──REST──▶  Go backend  ──Docker Engine API──▶  dockerd
   (Tailwind, Recharts)  ◀─WebSocket (live stats + logs)─┘
```

The Go server embeds the built SPA (`go:embed`) and serves everything from one
origin, so the production artifact is a single executable.

| Layer    | Technology |
|----------|------------|
| Backend  | Go, [chi](https://github.com/go-chi/chi), [coder/websocket](https://github.com/coder/websocket), official Docker SDK |
| Storage  | SQLite via [modernc.org/sqlite](https://modernc.org/sqlite) (pure Go, no CGO); metric history in Redis or memory |
| Auth     | Argon2id, TOTP ([pquerna/otp](https://github.com/pquerna/otp)), JWT |
| Frontend | React, TypeScript, Vite, Tailwind CSS, Recharts, React Flow, xterm.js |

## 🚀 Quick start

### Prerequisites
- Go ≥ 1.25
- Node.js ≥ 18 (only to build the UI)
- A running Docker daemon (access to `/var/run/docker.sock`)

### Build & run

```bash
git clone <your-fork-url> docker-commander
cd docker-commander
make build      # builds the UI, then the binary with the UI embedded
./dockercmd     # serves on http://127.0.0.1:8080
```

Open <http://127.0.0.1:8080>, create the admin account, and scan the QR code to
enable 2FA. Done.

### Cross-compile for release

```bash
make release    # static binaries for linux/macos/windows in dist-bin/
```

## ⚙️ Configuration

All options have flags and environment-variable equivalents:

| Flag            | Env             | Default            | Description                              |
|-----------------|-----------------|--------------------|------------------------------------------|
| `-addr`         | `DC_ADDR`       | `127.0.0.1:8080`   | Listen address. Bind beyond loopback only deliberately. |
| `-data-dir`     | `DC_DATA_DIR`   | OS user config dir | Stores the SQLite DB and signing secret. |
| `-session-ttl`  | —               | `12h`              | Session token lifetime.                  |
| `-dev`          | `DC_DEV=1`      | off                | Dev mode: API only + permissive CORS for Vite. |
| `-metrics-token`| `DC_METRICS_TOKEN` | (empty/open)    | If set, `/metrics` requires `Authorization: Bearer <token>` (or `?token=`). |
| `-redis-addr`   | `DC_REDIS_ADDR` | (empty)            | Redis `host:port` for metric history; empty uses an in-memory ring buffer. |
| `-redis-password` | `DC_REDIS_PASSWORD` | (empty)       | Redis password (if required). `DC_REDIS_DB` selects the DB index. |
| `-metrics-retention` | `DC_METRICS_RETENTION` | `6h`     | How long to keep metric history (e.g. `30m`, `24h`). |

The Docker connection honours standard `DOCKER_HOST` / `DOCKER_CERT_PATH`
environment variables.

## 🖥️ Run as a service (Linux / systemd)

The server keeps monitoring, alerting and metric history running 24/7 whether
or not a browser is connected, so on a server you'll want it under systemd.

```bash
# 1. install the binary
sudo install -m755 dockercmd /usr/local/bin/dockercmd

# 2. dedicated user with docker socket access
sudo useradd --system --no-create-home --shell /usr/sbin/nologin dockercmd
sudo usermod -aG docker dockercmd

# 3. optional environment file
sudo install -d /etc/dockercmd
sudo cp deploy/dockercmd.env.example /etc/dockercmd/dockercmd.env   # then edit

# 4. install + start the unit (creates /var/lib/dockercmd via StateDirectory)
sudo cp deploy/dockercmd.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now dockercmd
```

It listens on loopback by default — put it behind a TLS reverse proxy (nginx,
Caddy) to expose it. Keep the **localhost 2FA exemption off** for server
deployments (Settings → it trusts `RemoteAddr`).

## 📈 Monitoring & alerting

Define alert rules in the **Alerts** screen. Each rule has a type:

| Type       | Fires when… | Config |
|------------|-------------|--------|
| `state`    | a container emits a lifecycle event | which events (die, kill, oom, stop, unhealthy) |
| `resource` | CPU% or MEM% crosses a threshold for N seconds | metric, operator, threshold, duration |
| `log`      | a log line matches | substring or regular expression |
| `restart`  | a container restarts too often | count within a time window (crash-loop) |

Rules target containers by name substring (blank = all), carry a severity and a
cooldown, and can attach a **webhook**. Webhook bodies are Go templates with
`{{.RuleName}} {{.Severity}} {{.Type}} {{.Container}} {{.Message}} {{.Value}}
{{.Time}}`; with no template the alert is sent as JSON.

**Grafana / Prometheus:** scrape `http://<host>:<port>/metrics` to graph
`dockercmd_container_cpu_percent`, `_mem_bytes`, `_mem_percent`, and
`_container_running` (labelled by container `id` and `name`).

## 🧑‍💻 Development

Run the backend in dev mode and the Vite dev server side by side:

```bash
# terminal 1 — API on :8080
make dev

# terminal 2 — UI on :5173 (proxies /api to :8080)
cd web && npm install && npm run dev
```

Run the tests and static checks:

```bash
make test
make vet
```

## 🔒 Security notes

- Designed for **local installation by default** (binds to loopback).
- If you expose it on a server, put it behind TLS (reverse proxy) — the session
  cookie is `HttpOnly` + `SameSite=Strict`, and 2FA is mandatory by default.
- **2FA is enforced everywhere** unless an admin enables the *localhost
  exemption* in Settings, which lets loopback (127.0.0.1/::1) connections log in
  with a password only. Remote connections always require 2FA. The exemption
  trusts `RemoteAddr` only — keep it **off** behind a reverse proxy.
- **Roles**: `admin` accounts manage users, settings and have full access;
  `user` accounts are limited to granted menu sections and can be read-only.
- The JWT signing secret is generated on first run and persisted in the data dir.
- Registry credentials are **encrypted at rest** (AES-256-GCM) with a key
  generated on first run; the API never returns stored secrets.
- **SSH remote hosts** verify the daemon's host key against `~/.ssh/known_hosts`
  or a key you explicitly trust on first connect (pinned in the DB). A key that
  later changes is refused as a possible man-in-the-middle until you re-trust it.

## 🗺️ Roadmap

- [x] Connectivity topology graph (containers ↔ networks)
- [x] Aggregated multi-container log view with level detection
- [x] Alerting (state / resource / log / restart) + webhooks + Prometheus export
- [x] Interactive container console (exec)
- [x] Remote host management UI (TCP+TLS, SSH) + host switcher
- [x] Historical metrics storage & charts (Redis or in-memory)
- [x] SSH host-key verification (known_hosts + trust-on-first-use pinning, MITM-safe)
- [x] Images management (list / pull with live progress / remove / prune)
- [x] Inspect & observe: raw inspect (any object), container diff/top, image history, live events feed, disk usage (df)
- [x] Registry & build: push / private pull (encrypted stored creds), tag, and image build with context upload
- [x] Image transfer: save / load (tar), import, container export
- [x] Container lifecycle: create/run, rename, update limits, commit, restart-policy
- [x] Volumes management + inspector, and which containers use them
- [x] File browser inside containers — list / download / upload / delete (`docker cp`)
- [x] Email/SMTP notification channel
- [x] Structured log views & saved parsing rules
- [x] Multi-user accounts with roles, per-section permissions & read-only mode
- [x] Global feature flags (disable whole sections) + optional localhost 2FA exemption
- [x] LDAP / Active Directory authentication with auto-provisioning

> Working notes & "continue here" plan: see [NEXT.md](./NEXT.md).

## 📄 License

MIT (suggested) — add a `LICENSE` file for your chosen license.
