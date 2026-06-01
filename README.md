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
- **Container control** — start, stop, restart, pause, kill.
- **Interactive console** — a real shell (xterm.js) into any running container,
  streamed over a WebSocket; works the same on local and remote hosts.
- **Multi-host** — manage local, **TCP(+TLS)** and **SSH** Docker hosts and
  switch between them; every view and stream rebinds to the selected host.
- **Live logs** — follow `stdout`/`stderr`, filter by substring, toggle streams.
- **Aggregated logs** — a global Logs view streaming many containers at once,
  color-coded by source with automatic log-level detection and level filtering.
- **Container detail** — image, command, env vars, mounts, ports, networks,
  health, restart policy.
- **Networks & topology** — networks with drivers/subnets/scope, plus an
  interactive **connectivity graph** (containers ↔ networks) you can pan/zoom.
- **Alerting** — user-defined rules on container **state changes**, **resource
  thresholds** (CPU/MEM), **log patterns** (substring/regex), and **restart /
  crash loops**, with per-rule severity and cooldown.
- **Notifications & export** — fire alerts to **generic webhooks** (Slack,
  Discord, Grafana alerting, n8n…) with Go-template payloads, an **in-app alert
  feed**, and a **Prometheus `/metrics`** exporter for Grafana dashboards.
- **Audit log** — every privileged action is recorded (who, what, when, from where).
- **Security first** — password login with **Argon2id**, **mandatory TOTP 2FA**,
  signed session cookies, login rate limiting, strict security headers.
- **Multi-host ready** — connect to the local daemon today; remote TCP+TLS hosts
  are modeled in the data layer for upcoming releases.

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
  cookie is `HttpOnly` + `SameSite=Strict`, and 2FA is mandatory.
- The JWT signing secret is generated on first run and persisted in the data dir.

## 🗺️ Roadmap

- [x] Connectivity topology graph (containers ↔ networks)
- [x] Aggregated multi-container log view with level detection
- [x] Alerting (state / resource / log / restart) + webhooks + Prometheus export
- [x] Interactive container console (exec)
- [x] Remote host management UI (TCP+TLS, SSH) + host switcher
- [x] Historical metrics storage & charts (Redis or in-memory)
- [ ] **SSH known_hosts verification** (currently trusts on first connect) — required before exposing remote hosts
- [ ] Images management (list / pull / remove / prune)
- [ ] Volumes management + inspector, and which containers use them
- [ ] Data transfer to/from containers (`docker cp` up/download)
- [ ] Email/SMTP notification channel
- [ ] Structured log views & saved parsing rules

> Working notes & "continue here" plan: see [NEXT.md](./NEXT.md).

## 📄 License

MIT (suggested) — add a `LICENSE` file for your chosen license.
