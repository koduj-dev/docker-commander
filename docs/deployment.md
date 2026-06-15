# Deployment

[← Manual index](README.md)

Docker Commander is a single binary that embeds the UI. The server runs
monitoring, alerting and metric history **continuously** — independent of any
connected browser — so on a server you'll want it supervised.

## Configuration
Every option is a flag with a `DC_*` environment-variable equivalent, and can
live in a config file. See
[`deploy/commander.conf.example`](../deploy/commander.conf.example) for the full
list. Key ones:

| Env | Default | Purpose |
|-----|---------|---------|
| `DC_HOST` | `127.0.0.1` | listen host/interface (use `0.0.0.0` for all; keep on loopback behind a proxy) |
| `DC_PORT` | `8470` | listen port (also `-p 9000` shorthand) |
| `DC_ADDR` | (unset) | legacy full `host:port`; overrides `DC_HOST`/`DC_PORT` if set |
| `DC_TLS_CERT` / `DC_TLS_KEY` | (off) | PEM cert + key paths; set both to serve **HTTPS** directly |
| `DC_MCP_ENABLED` | (off) | enable the remote **MCP** server for AI tools (off by default; serve behind HTTPS) — see [MCP](mcp.md) |
| `DC_MCP_PUBLIC_URL` | (unset) | externally reachable base URL (`https://host`) — required for the MCP **OAuth** flow (bearer tokens work without it) |
| `DC_DATA_DIR` | OS config dir | SQLite DB + signing/encryption keys |
| `DC_METRICS_TOKEN` | (open) | bearer token guarding `/metrics` |
| `DC_REDIS_ADDR` | (memory) | Redis for metric history |
| `DC_METRICS_RETENTION` | `6h` | history retention |
| `DC_METRICS_INTERVAL` | `15s` | how often the monitor samples every running container's stats — **raise it** (e.g. `30s`/`60s`) on a host with many containers if the sampling sweep is costly |
| `DC_TRUSTED_PROXIES` | (none) | comma-separated reverse-proxy IPs/CIDRs whose `X-Forwarded-For` is trusted for the real client IP — **set this when behind a proxy** (see below) |
| `DC_UPDATE_CHECK` | `1` | check GitHub Releases for a newer version (admin banner); set `0` to disable the outbound call |
| `DC_PPROF` | (off) | serve Go's `net/http/pprof` on a **dedicated `127.0.0.1:6060`** listener for profiling; off in normal operation |

> **Diagnosing high CPU.** Enable `DC_PPROF=1` and the app starts a profiling
> server bound **only to loopback** (`127.0.0.1:6060`) — separate from the main
> port, so it is never reachable off-box no matter what interface the app binds
> or what `X-Forwarded-For` a client sends. From the server (or through an SSH
> tunnel) capture a profile:
>
> ```bash
> go tool pprof -top -seconds=30 http://127.0.0.1:6060/debug/pprof/profile
> ```
>
> The biggest steady cost is usually the per-interval **stats sweep** over all
> running containers (also driven by the Docker daemon itself); raising
> `DC_METRICS_INTERVAL` is the first lever on a container-dense host.

> **Client IP & reverse proxies.** Every IP-based decision — login / OAuth
> **rate limits**, the **loopback 2FA exemption**, and **audit** entries — uses
> the connecting client's address. By default Docker Commander trusts **only the
> real TCP peer** and **ignores** `X-Forwarded-For`, so a client can't forge its
> address (e.g. claim loopback to skip 2FA, or rotate IPs to evade
> brute-force throttling). When you run behind a reverse proxy, set
> `DC_TRUSTED_PROXIES` to the proxy's address(es) (e.g. `127.0.0.1/32,::1/128`)
> so the **real** client IP is read from `X-Forwarded-For` — only then, and only
> for connections coming **from** those proxies. Leave it unset if the app is
> exposed directly.

Docker connection honours `DOCKER_HOST` / `DOCKER_CERT_PATH`.

### Config file
When running as a service, the simplest place for settings is a config file. It
is a plain `KEY=VALUE` file using the same `DC_*` keys; `#` starts a comment and
`export `/quotes are tolerated. (Flags and env vars still work and take
precedence, but the config file is the recommended single source of truth.)

```ini
# /etc/docker-commander/commander.conf
DC_HOST=127.0.0.1
DC_PORT=8470
DC_DATA_DIR=/var/lib/dockercmd
DC_METRICS_RETENTION=24h
```

The binary reads **`/etc/docker-commander/commander.conf`** by default (on
Unix); point it elsewhere with `-config /path/to/file` or `$DC_CONFIG`. A
missing default file is ignored; a missing **explicit** one is an error.
**Precedence:** command-line flag → environment variable → config file →
built-in default. A starter file lives at
[`deploy/commander.conf.example`](../deploy/commander.conf.example).

## Running as a service

### The binary installs itself (Linux / macOS)
The simplest path — the binary writes the service definition for the current OS,
installs itself to a stable location, and starts it. No script, no manual steps:

```bash
sudo ./dockercmd --install-service     # Linux  — systemd (needs root)
./dockercmd --install-service          # macOS  — launchd LaunchAgent (your user, NOT sudo)

dockercmd --service-status             # show service status
sudo dockercmd --uninstall-service     # stop + remove (keeps the data dir)
```

On **Linux** it creates the dedicated `dockercmd` user in the `docker` group,
copies itself to `/usr/local/bin/dockercmd`, installs the hardened unit and
`enable --now`s it. On **macOS** it installs a per-user LaunchAgent under
`~/Library` (no sudo — a system daemon can't reach Docker Desktop's user-owned
socket). Uninstall leaves the data dir and user in place so reinstalling keeps
the database and keys. **Windows** isn't covered by the subcommand yet — use the
script below.

Installing also drops a **`man dockercmd`** page (under
`/usr/local/share/man/man1/`), so the full option/action reference is available
offline once the service is in place.

> **Discovering the CLI.** `dockercmd --help` (or `-h`) prints a complete usage
> — a synopsis, the **standalone actions** (`--version`, `--self-upgrade`,
> `--install-service` / `--uninstall-service` / `--service-status`) and every
> option with its default. `dockercmd --version` (or `dockercmd version`) prints
> the build version.

### Debian / Ubuntu & Fedora packages (.deb / .rpm)
Each release also publishes `.deb` and `.rpm` packages (amd64 + arm64) on the
[Releases](../../releases) page. They install the binary to `/usr/bin/dockercmd`,
a hardened **systemd** unit, the man page, and a config at
`/etc/docker-commander/commander.conf` (a *conffile* — your edits survive
upgrades), then create the `dockercmd` user and start the service:

```bash
sudo apt install ./dockercmd_<version>_amd64.deb     # Debian / Ubuntu
sudo dnf install ./dockercmd-<version>.x86_64.rpm     # Fedora / RHEL
```

Or add the **signed APT repository** (GPG-signed, served from GitHub Pages) and
let `apt` keep it updated:

```bash
curl -fsSL https://koduj-dev.github.io/apt/key.asc \
  | sudo tee /etc/apt/keyrings/dockercmd.asc >/dev/null
echo "deb [signed-by=/etc/apt/keyrings/dockercmd.asc] https://koduj-dev.github.io/apt stable main" \
  | sudo tee /etc/apt/sources.list.d/dockercmd.list >/dev/null
sudo apt update && sudo apt install dockercmd
```

### Installer scripts (alternative; Windows)
Equivalent idempotent installers also live in [`deploy/`](../deploy/) — handy for
Windows, or to read exactly what gets installed:

| OS | Command | Mechanism |
|----|---------|-----------|
| **Linux**   | `sudo ./deploy/install-linux.sh ./dockercmd` | systemd unit |
| **macOS**   | `./deploy/install-macos.sh ./dockercmd` (your user, **not** sudo) | launchd LaunchAgent |
| **Windows** | `.\deploy\install-windows.ps1 -BinPath .\dockercmd.exe` (elevated PowerShell) | Scheduled Task |

Each script finds the binary automatically if you drop the release next to it
(`dockercmd`, or `dockercmd-<os>-<arch>`), installs it, writes the service
definition, and starts it. Then create the admin account in the UI — on the
address from your config (`DC_HOST`/`DC_PORT`/`DC_TLS_*`; default
<http://127.0.0.1:8470>).

### Linux (systemd)
`install-linux.sh` creates a dedicated `dockercmd` system user in the `docker`
group, installs the binary to `/usr/local/bin`, seeds
`/etc/docker-commander/commander.conf` (only if absent), creates the
`/var/lib/dockercmd` data dir, installs the
[hardened unit](../deploy/dockercmd.service), and `enable --now`s it. The unit
runs with `NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome=true` and a
private `StateDirectory`.

<details>
<summary>Manual steps (what the installer does)</summary>

```bash
sudo install -m755 dockercmd /usr/local/bin/dockercmd
sudo useradd --system --no-create-home --shell /usr/sbin/nologin dockercmd
sudo usermod -aG docker dockercmd
sudo install -d /etc/docker-commander && sudo cp deploy/commander.conf.example /etc/docker-commander/commander.conf   # edit
sudo cp deploy/dockercmd.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now dockercmd
```
</details>

### macOS (launchd)
`install-macos.sh` installs a **per-user LaunchAgent**
(`~/Library/LaunchAgents/dev.koduj.dockercmd.plist`), not a system LaunchDaemon —
with Docker Desktop the daemon socket is owned by the logged-in user, so a root
daemon usually can't reach it. The agent starts at login and is restarted
automatically (`KeepAlive`); logs go to `~/Library/Logs/dockercmd.log`.

### Windows (Scheduled Task)
The binary is a plain console program, not a native Windows service (no Service
Control Manager handshake), so `sc.exe create` / `New-Service` fail with error
1053. `install-windows.ps1` instead registers a **Scheduled Task** that starts it
at boot (or `-AtLogon`, if Docker Desktop only runs under your account) and
restarts it on failure. For a "real" service, wrap the exe with
[NSSM](https://nssm.cc) or WinSW — see the script header.

> **Compose/Projects disabled under systemd?** If the **Projects** page warns
> that "the `docker compose` CLI isn't available", it's the `ProtectHome=true`
> hardening: it makes the service user's home inaccessible, which breaks the
> docker CLI's plugin discovery. The shipped unit fixes this with
> `Environment=DOCKER_CONFIG=/var/lib/dockercmd/.docker` (a writable config dir
> outside the protected home). If you wrote your own unit, add that line and
> `systemctl daemon-reload && systemctl restart dockercmd`.

## Health check
`GET /healthz` (alias `/health`) is an unauthenticated probe for load
balancers, uptime monitors and Kubernetes. It returns `200` with
`{"status":"ok","version":"…"}` when the DB is reachable, `503` otherwise. The
running build version is also shown in the UI sidebar footer and at
`GET /api/version`.

## Logs
Docker Commander logs to **stderr**, so under systemd everything goes to the
**journal**:

```bash
journalctl -u dockercmd -f          # follow
journalctl -t dockercmd --since today
```

Every **fired alert** is written as a structured line, so failures are visible
in your log pipeline, not only in the in-app feed:

```
alert severity=critical rule="db down" host="prod-1" container="postgres" message="container event: die"
```

To forward the journal to a **syslog** daemon (rsyslog/syslog-ng → SIEM), set
`ForwardToSyslog=yes` in `/etc/systemd/journald.conf` and restart
`systemd-journald`. Entries are tagged `dockercmd` (`SyslogIdentifier`). Not
using systemd? Redirect the process's stderr to a file or your collector.

## HTTPS
Two options:

**A — native TLS (no proxy).** Point Docker Commander at a PEM cert + key and it
serves HTTPS directly — handy for a small public deployment:

```ini
DC_HOST=0.0.0.0
DC_PORT=8470
DC_TLS_CERT=/etc/docker-commander/tls/cert.pem
DC_TLS_KEY=/etc/docker-commander/tls/key.pem
```

Use a real certificate (e.g. Let's Encrypt) for public hosts; both keys must be
set together (TLS ≥ 1.2). The key file should be readable only by the service
user.

**B — reverse proxy (recommended for anything non-trivial).**
Bind to loopback and terminate TLS at nginx/Caddy. WebSockets must be allowed
(stats, logs, exec, events) — proxy `Upgrade`/`Connection` headers. Example
(nginx) for a location:

```nginx
location / {
    proxy_pass http://127.0.0.1:8470;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host;
}
```

> Behind a proxy, **keep the localhost 2FA exemption off** — it trusts the
> connection address, which becomes the proxy. See [Settings](settings.md).

## Self-update
Docker Commander compares the running build against the latest **GitHub
Release** and shows an admin **"update available"** banner when a newer version
exists. The check is cached and runs server-side; set `DC_UPDATE_CHECK=0` to
disable the outbound call on air-gapped hosts.

To upgrade the binary:

```bash
dockercmd --self-upgrade           # download, verify SHA-256, replace in place
dockercmd --self-upgrade --check   # only report whether an update is waiting
```

`--self-upgrade` fetches the release asset for your OS/arch, **verifies its
SHA-256**, and atomically replaces the running binary (preserving its
permissions). The binary must be writable by the invoking user; **restart** the
service afterwards to run the new version. (Installed from a package manager?
Update through that instead.)

## Backup
Back up `DC_DATA_DIR` — it holds the SQLite database and the keys that sign
sessions and encrypt stored secrets. Losing the keys means re-entering registry
/ SMTP / LDAP passwords.
