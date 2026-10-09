# Deployment

[← Manual index](README.md)

Installing, configuring, securing and upgrading the server. Docker Commander is a
single binary with the UI embedded. It runs monitoring, alerting and metric history
**continuously**, whether or not a browser is open, so on a server run it as a
supervised service.

## Common tasks

**Install it as a service on Linux.** Run `sudo ./dockercmd --install-service`.
It creates the `dockercmd` user, copies itself to `/usr/local/bin`, installs a
hardened systemd unit and starts it. Then open <http://127.0.0.1:8470> and create
the admin account. Packages and other OSes: [Running as a service](#running-as-a-service).

**Serve HTTPS with Let's Encrypt.** On a public host with no proxy in front, set
`DC_ACME_DOMAINS=docker.example.com`, listen on `0.0.0.0:443` and restart. DNS must
already point at the host. Details and requirements: [HTTPS](#https), option A2.

**Run it behind nginx or Caddy.** Keep `DC_HOST=127.0.0.1`, proxy with WebSocket
headers, and set `DC_TRUSTED_PROXIES` to the proxy's address so rate limits and
audit see the real client IP. See [HTTPS](#https), option B.

**Upgrade.** Click **Update & restart** on the admin banner, or run
`dockercmd --self-upgrade` and restart the service. Installed from a `.deb`/`.rpm`
or APT? Upgrade through the package manager. See [Self-update](#self-update).

**Reset a lost admin password.** On the server:
`sudo dockercmd --data-dir /var/lib/dockercmd --reset-password admin`. No need to
stop the service. See [Locked out](#locked-out).

**Move to a new server.**

1. Old server: `sudo dockercmd --data-dir /var/lib/dockercmd --backup dc.tar.gz --passphrase`.
2. Install Docker Commander on the new server, then `sudo systemctl stop dockercmd`.
3. `sudo dockercmd --data-dir /var/lib/dockercmd --restore dc.tar.gz --passphrase --force`.
   Installing started the service once, so an empty database already exists and
   needs `--force` to be replaced.
4. `sudo chown -R dockercmd: /var/lib/dockercmd`, so the service user owns the
   restored files, then `sudo systemctl start dockercmd`.

Users, settings, projects and keys come across as they were. See
[Backup & restore](#backup--restore).

**Find out why it uses a lot of CPU.** Set `DC_PPROF=1` and take a profile
([Profiling](#profiling)). Usually the stats sweep dominates; raise
`DC_METRICS_INTERVAL` (e.g. `30s`).

**Route project domains through the built-in proxy.** With ACME mode on, set
`DC_PROXY_ENABLED=1`. See [Embedded reverse proxy](#embedded-per-container-reverse-proxy).

## Configuration

Nearly every option is a flag with a `DC_*` environment-variable equivalent and can
live in a config file. Two exceptions: **`-session-ttl`** (how long a sign-in lasts,
default **12h**) is flag-only, and `DC_REDIS_DB` has no flag (environment or config
file only). [`deploy/commander.conf.example`](../deploy/commander.conf.example) lists
every key, and `dockercmd --help` every flag. The key ones:

| Env | Default | Purpose |
|-----|---------|---------|
| `DC_HOST` | `127.0.0.1` | listen host/interface (use `0.0.0.0` for all; keep on loopback behind a proxy) |
| `DC_PORT` | `8470` | listen port (also `-p 9000` shorthand) |
| `DC_ADDR` | (unset) | legacy full `host:port`; overrides `DC_HOST`/`DC_PORT` if set |
| `DC_TLS_CERT` / `DC_TLS_KEY` | (off) | PEM cert + key paths; set both to serve **HTTPS** directly |
| `DC_ACME_DOMAINS` | (off) | comma-separated public hostname(s): serve **HTTPS** via automatic ACME/Let's Encrypt certificates instead of a static pair — see [HTTPS](#https) |
| `DC_ACME_EMAIL` / `DC_ACME_CACHE_DIR` / `DC_ACME_DIRECTORY_URL` | (unset) / `<data-dir>/acme` / Let's Encrypt | ACME contact, certificate cache and CA directory — see [HTTPS](#https), option A2 |
| `DC_PROXY_ENABLED` | (off) | enable the embedded **per-container reverse proxy** for `domain_mappings` (local-host projects only; requires `DC_ACME_DOMAINS`) — see [Projects → Domains](projects.md#domains) |
| `DC_PROXY_ACME_CACHE_DIR` | `<data-dir>/proxy-acme` | cache dir for the proxy's own ACME certificate/account state, kept separate from `DC_ACME_CACHE_DIR` |
| `DC_MCP_ENABLED` | (off) | enable the remote **MCP** server for AI tools (off by default; serve behind HTTPS) — see [MCP](mcp.md) |
| `DC_MCP_PUBLIC_URL` | (unset) | externally reachable base URL (`https://host`) — required for the MCP **OAuth** flow (bearer tokens work without it) |
| `DC_DATA_DIR` | OS config dir | SQLite DB + signing/encryption keys |
| `DC_METRICS_TOKEN` | (open) | token guarding `/metrics`, sent as a Bearer header or `?token=` |
| `DC_REDIS_ADDR` | (memory) | Redis for metric history; empty keeps the in-memory ring buffer |
| `DC_REDIS_PASSWORD` | (empty) | Redis password, if the server requires auth |
| `DC_REDIS_DB` | `0` | Redis database index |
| `DC_METRICS_RETENTION` | `6h` | history retention |
| `DC_METRICS_INTERVAL` | `15s` | how often the monitor samples every running container's stats — **raise it** (e.g. `30s`/`60s`) on a host with many containers if the sampling sweep is costly; `0` or less means `15s` |
| `DC_TRUSTED_PROXIES` | (none) | comma-separated reverse-proxy IPs/CIDRs whose `X-Forwarded-For` is trusted for the real client IP — **set this when behind a proxy** (see below) |
| `DC_UPDATE_CHECK` | `1` | check GitHub Releases for a newer version (admin banner); set `0` to disable the outbound call |
| `DC_SELF_UPDATE` | `1` | allow admins to apply an update from the web UI (the one-tap "Update & restart"); set `0` to keep the banner but forbid web-triggered self-replacement |
| `DC_LOG_FILE` | (stderr) | write the log to this file instead, rotated at 10 MiB with one older copy (`<file>.1`). Under systemd the journal already has it |
| `DC_PPROF` | (off) | serve Go's `net/http/pprof` on a **dedicated `127.0.0.1:6060`** listener for profiling; off in normal operation |
| `DC_DEPLOY_SILENCE_GRACE` | `3m` | silence alert delivery for a project this long after a successful deploy; `0` disables — see [Alerts](alerts.md) |
| `DC_DEV` | (off) | development mode: serves the API only (no embedded UI; run the Vite dev server) and allows cross-origin requests from the dev server. Not for production |

**On/off values.** `DC_DEV`, `DC_MCP_ENABLED`, `DC_PROXY_ENABLED` and `DC_PPROF` are on
only for exactly `1`; `true` or `yes` leaves them off. `DC_UPDATE_CHECK` and
`DC_SELF_UPDATE` are off only for exactly `0`. A port, number or duration that doesn't
parse (e.g. `DC_METRICS_RETENTION=1d`) silently falls back to the default.

The Docker connection honours `DOCKER_HOST` / `DOCKER_CERT_PATH`.

**Client IP and reverse proxies.** Login/OAuth **rate limits**, the **loopback 2FA
exemption** and **audit** entries all use the client's address. By default only the
**real TCP peer** is trusted and `X-Forwarded-For` is **ignored**, so a client can't
forge its address (claim loopback to skip 2FA, or rotate IPs to dodge brute-force
throttling). Behind a proxy, set `DC_TRUSTED_PROXIES` to its address(es), e.g.
`127.0.0.1/32,::1/128`. The real client IP is then read from `X-Forwarded-For`, but
only on connections **from** those proxies. Leave it unset if the app is exposed
directly.

### Profiling

With `DC_PPROF=1`, the profiling server listens **only on loopback**
(`127.0.0.1:6060`), separate from the main port, so it is never reachable off-box
whatever interface the app binds or whatever `X-Forwarded-For` a client sends.
Capture a profile on the server (or through an SSH tunnel):

```bash
go tool pprof -top -seconds=30 http://127.0.0.1:6060/debug/pprof/profile
```

The biggest steady cost is usually the per-interval **stats sweep** over running
containers (partly in the Docker daemon itself).

### Config file

The simplest place for settings when running as a service. A plain `KEY=VALUE`
file with the same `DC_*` keys; `#` starts a comment, `export ` and quotes are
tolerated.

```ini
# /etc/docker-commander/commander.conf
DC_HOST=127.0.0.1
DC_PORT=8470
DC_DATA_DIR=/var/lib/dockercmd
DC_METRICS_RETENTION=24h
```

- Default path (Unix): **`/etc/docker-commander/commander.conf`**. Override with
  `-config /path/to/file` or `$DC_CONFIG`.
- A missing default file is ignored; a missing **explicit** one is an error.
- **Precedence:** command-line flag → environment variable → config file →
  built-in default. Flags and env vars still work, but the config file is the
  recommended single source of truth.
- Starter file: [`deploy/commander.conf.example`](../deploy/commander.conf.example).

## Running as a service

### The binary installs itself (Linux / macOS / Windows)

The binary writes the service definition for the current OS, installs itself to a
stable location and starts it:

```bash
sudo ./dockercmd --install-service     # Linux    — systemd (needs root)
./dockercmd --install-service          # macOS    — launchd LaunchAgent (your user, NOT sudo)
dockercmd.exe --install-service        # Windows  — SCM service (elevated PowerShell/cmd)

dockercmd --service-status             # show service status
sudo dockercmd --uninstall-service     # stop + remove (keeps the data dir)
```

| OS | What it does |
|----|--------------|
| **Linux** | Creates the `dockercmd` user in the `docker` group, copies itself to `/usr/local/bin/dockercmd`, installs the hardened unit and `enable --now`s it. |
| **macOS** | Installs a per-user LaunchAgent under `~/Library`. No sudo: a system daemon can't reach Docker Desktop's user-owned socket. |
| **Windows** | Copies itself to `%ProgramFiles%\docker-commander\dockercmd.exe`, registers a Service Control Manager (SCM) service with auto-restart on failure, and starts it. See [Windows](#windows-native-service-or-scheduled-task). |

Uninstall keeps the data dir (and on Linux the service user), so a reinstall keeps
the database and keys. Installing also adds a **`man dockercmd`** page (under
`/usr/local/share/man/man1/`). `dockercmd --help` (or `-h`) prints the full usage:
the **standalone actions** (`--version`, `--make-certs`, `--self-upgrade`, `--backup`,
`--restore`, `--reset-password`, `--install-service` / `--uninstall-service` /
`--service-status`) and every option with its default.
`dockercmd --version` (or `dockercmd version`) prints the build version.

### Debian / Ubuntu & Fedora packages (.deb / .rpm)

Each release publishes `.deb` and `.rpm` packages (amd64 + arm64) on the
[Releases](https://github.com/koduj-dev/docker-commander/releases) page. They install the binary to `/usr/bin/dockercmd`, a
hardened **systemd** unit, the man page, and `/etc/docker-commander/commander.conf`
(a *conffile*: your edits survive upgrades), then create the `dockercmd` user and
start the service.

```bash
sudo apt install ./dockercmd_<version>_amd64.deb     # Debian / Ubuntu
sudo dnf install ./dockercmd-<version>.x86_64.rpm     # Fedora / RHEL
```

Or use the **signed APT repository** (GPG-signed, served from GitHub Pages) so `apt`
keeps it updated:

```bash
curl -fsSL https://koduj-dev.github.io/apt/key.asc \
  | sudo tee /etc/apt/keyrings/dockercmd.asc >/dev/null
echo "deb [signed-by=/etc/apt/keyrings/dockercmd.asc] https://koduj-dev.github.io/apt stable main" \
  | sudo tee /etc/apt/sources.list.d/dockercmd.list >/dev/null
sudo apt update && sudo apt install dockercmd
```

### Installer scripts (alternative)

Idempotent installers in [`deploy/`](../deploy/) do the same job. Useful to read
exactly what gets installed, or on Windows if you prefer a Scheduled Task to the
SCM service.

| OS | Command | Mechanism |
|----|---------|-----------|
| **Linux**   | `sudo ./deploy/install-linux.sh ./dockercmd` | systemd unit |
| **macOS**   | `./deploy/install-macos.sh ./dockercmd` (your user, **not** sudo) | launchd LaunchAgent |
| **Windows** | `.\deploy\install-windows.ps1 -BinPath .\dockercmd.exe` (elevated PowerShell) | Scheduled Task |

Each script finds the binary if the release sits next to it (`dockercmd` or
`dockercmd-<os>-<arch>`), installs it, writes the service definition and starts it.
Then create the admin account in the UI, at the address from your config
(`DC_HOST`/`DC_PORT`/`DC_TLS_*`; default <http://127.0.0.1:8470>).

### Linux (systemd)

`install-linux.sh` also seeds `/etc/docker-commander/commander.conf` (only if
absent) and creates the `/var/lib/dockercmd` data dir. The
[hardened unit](../deploy/dockercmd.service) runs with `NoNewPrivileges`,
`ProtectSystem=strict`, `ProtectHome=true` and a private `StateDirectory`. Its
one capability is `CAP_NET_BIND_SERVICE`, so it can listen on 443; processes it
starts (`docker`, `docker compose`, `ssh`) inherit it too.

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

> **Projects page says "the `docker compose` CLI isn't available"?** That is
> `ProtectHome=true`: it hides the service user's home, which breaks the docker
> CLI's plugin discovery. The shipped unit fixes it with
> `Environment=DOCKER_CONFIG=/var/lib/dockercmd/.docker` (a writable config dir
> outside the protected home). In your own unit, add that line, then
> `systemctl daemon-reload && systemctl restart dockercmd`.

### macOS (launchd)

`install-macos.sh` installs a **per-user LaunchAgent**
(`~/Library/LaunchAgents/dev.koduj.dockercmd.plist`), not a system LaunchDaemon:
Docker Desktop's socket belongs to the logged-in user, so a root daemon usually
can't reach it. It starts at login and is restarted automatically (`KeepAlive`).
Logs go to `~/Library/Logs/dockercmd.log`.

### Windows (native service, or Scheduled Task)

`dockercmd.exe --install-service` (elevated prompt) registers a real **SCM**
service. It copies itself to `%ProgramFiles%\docker-commander\dockercmd.exe`,
creates the service (Automatic, delayed auto-start, SCM recovery actions restart it
on crash) and starts it. Data lives in `%ProgramData%\docker-commander\data`, and
the log is `dockercmd.log` in that folder (rotated at 10 MiB, one older copy kept as
`dockercmd.log.1`). `dockercmd --service-status` prints its path.

`install-windows.ps1` is a dependency-free alternative that registers a **Scheduled
Task**. It starts at boot (or `-AtLogon`, if Docker Desktop only runs under your
account) and restarts on failure. By default the task runs as SYSTEM; `-AtLogon`
runs it as your account instead. Use it if you want to read exactly what gets
installed. The task passes `-log-file`, so the log is `dockercmd.log` in the data
dir, as with the native service. Wrapping the exe with
[NSSM](https://nssm.cc) or WinSW still works, but is no longer needed for a real
service.

**Pick one.** Both at once means two copies racing over the same data dir and port.
Each installer refuses to run if the other is installed (it checks for the SCM
service `dockercmd` or the Scheduled Task `DockerCommander`), and fails **closed**
if it can't tell (permissions, unreachable SCM/Task Scheduler). To switch, stop and
remove the old one first.

**Data dir permissions.** The data dir holds the database, TLS private keys and the
at-rest encryption key. Its ACL is set on **every startup**, not just at install:
`SYSTEM` and `Administrators` get Full Control, nothing else (the inherited
`%ProgramData%` default could leave it readable by any local account). This applies
to the SCM service, the Scheduled Task, and a console run alike. A
non-Administrator account running it directly is added too, so it doesn't lock
itself out.

`--install-service` also checks an *existing* data dir and refuses to reinstall
over it, rather than silently "fixing" it, if the dir grants access beyond
`SYSTEM`/`Administrators`/`CREATOR OWNER`, is **owned** by anything else than
`SYSTEM`/`Administrators` (an owner can always rewrite its own ACL), or is a
**reparse point** (a symlink or junction that could redirect a privileged
process). Inspect it by hand first.

## Health check

`GET /healthz` (alias `/health`) is an unauthenticated probe for load balancers,
uptime monitors and Kubernetes. It returns `200` with
`{"status":"ok","version":"…"}` when the DB is reachable, `503` otherwise. The
running version is also in the UI sidebar footer and at `GET /api/version`.

## Logs

Docker Commander logs to **stderr**, so under systemd it all goes to the
**journal**:

```bash
journalctl -u dockercmd -f          # follow
journalctl -t dockercmd --since today
```

Every **fired alert** is also a structured log line, so failures show in your log
pipeline, not only in the app:

```
alert kind=firing severity=critical rule="db down" host="prod-1" container="postgres" message="container event: die"
```

An alert silenced by a maintenance window ends with `silenced=true
maintenance_window=… window_name=…`. Host-reachability alerts have no `kind` or
`container`.

To forward to **syslog** (rsyslog/syslog-ng → SIEM), set `ForwardToSyslog=yes` in
`/etc/systemd/journald.conf` and restart `systemd-journald`. Entries are tagged
`dockercmd` (`SyslogIdentifier`). Without systemd, redirect stderr to a file or
your collector.

## HTTPS

Three options: a static certificate, automatic Let's Encrypt, or a reverse proxy.

### A — native TLS with a static certificate

Docker Commander serves HTTPS directly from a PEM cert + key. Handy for a small
deployment with no proxy.

```ini
DC_HOST=0.0.0.0
DC_PORT=8470
DC_TLS_CERT=/etc/docker-commander/tls/cert.pem
DC_TLS_KEY=/etc/docker-commander/tls/key.pem
```

Set both together (TLS ≥ 1.2). Use a real certificate for public hosts, and make
the key readable only by the service user.

For a quick **self-signed** cert (LAN/internal) without `openssl`, run
`dockercmd --make-certs [hostnames…]`. It writes `cert.pem` + `key.pem` (key mode
0600) to `<data-dir>/tls/`, covering localhost plus the hosts you list, valid about
13 months, and prints the `DC_TLS_CERT` / `DC_TLS_KEY` to set. Like `--backup` it
doesn't read the config file: on a packaged install add `--data-dir /var/lib/dockercmd`.
Run as root, it also prints the `chown` that hands the files to the service user.
Clients warn until they trust the certificate.

### A2 — automatic HTTPS via ACME (Let's Encrypt)

For a public host with no reverse proxy in front. Docker Commander obtains and
renews a browser-trusted certificate itself. Mutually exclusive with
`DC_TLS_CERT`/`DC_TLS_KEY`.

```ini
DC_HOST=0.0.0.0
DC_PORT=443
DC_ACME_DOMAINS=docker.example.com
DC_ACME_EMAIL=ops@example.com
```

- `DC_ACME_DOMAINS` must be public **hostnames** the CA can verify, not IP
  addresses, and their DNS must already point at this host.
- **Port 443 under the service.** The unit runs as the unprivileged `dockercmd`
  user and grants it `CAP_NET_BIND_SERVICE`, the one capability needed to listen
  below port 1024. Packages pick the unit up on upgrade. An install made with
  `--install-service` before 1.7.0 has an older unit without it: run
  `sudo dockercmd --install-service` again, then `sudo systemctl restart dockercmd`.
- The port must be **directly** reachable from the internet on 443. The
  `tls-alpn-01` challenge is answered inside the TLS handshake, so no port-80
  listener is needed, but a proxy that terminates TLS itself would intercept it.
- `DC_ACME_EMAIL` is optional; the CA uses it for renewal and problem notices.
- Certificates are cached in `DC_ACME_CACHE_DIR` (default `<data-dir>/acme`), so a
  restart doesn't request a new one and eat into the CA's rate limits.
- **Testing:** point `DC_ACME_DIRECTORY_URL` at [Let's Encrypt's staging
  directory](https://letsencrypt.org/docs/staging-environment/). Its certificates
  aren't browser-trusted (that's the point), but its API has a normal trusted
  certificate. A local [Pebble](https://github.com/letsencrypt/pebble) instance does
  **not** work: its API certificate is untrusted, so the server refuses to talk to
  it, and its responses don't fit this server's certificate code path anyway (see
  [gotchas](gotchas.md)).

### Embedded per-container reverse proxy

Opt-in, on top of A2. With ACME mode active, `DC_PROXY_ENABLED=1` also routes
public traffic for a project's [domain mappings](projects.md#domains) to that
project's running container, on the same listener and port as the admin UI
(dispatched by SNI). It is off by default because it is a second public-facing
surface, and serves local-host projects only for now. Its certificates are cached
separately in `DC_PROXY_ACME_CACHE_DIR` (default `<data-dir>/proxy-acme`), so a
compromise of one cache can't expose the other's account key. Enabled without ACME
mode, it logs a clear message and changes nothing else.

### B — reverse proxy (recommended for anything non-trivial)

Bind to loopback and terminate TLS at nginx/Caddy. Allow WebSockets (stats, logs,
exec, events) by proxying the `Upgrade`/`Connection` headers. nginx example:

```nginx
location / {
    proxy_pass http://127.0.0.1:8470;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
}
```

**Set the last two headers.**

- `$proxy_add_x_forwarded_for` appends the real peer to what the client sent.
  Without it nginx passes the client's own `X-Forwarded-For` through, so anyone can
  claim any address, and that address keys your rate limits and audit records.
- `X-Forwarded-Proto` tells the app the connection was HTTPS, which marks the
  session cookie `Secure`.
- Both are believed **only** from an address in `DC_TRUSTED_PROXIES`, so set that
  too.

The **localhost 2FA exemption doesn't apply to a proxied request**: a proxy can't
vouch that someone is at the machine. A request counts as proxied when its peer is
listed in `DC_TRUSTED_PROXIES`, or when it carries any forwarding header
(`Forwarded`, `X-Forwarded-For`/`-Host`/`-Proto`/`-Server`, `X-Real-Ip`, `Via`),
even an empty one. A local proxy that is not listed and adds none of those headers
makes every client look local, so behind a proxy set `DC_TRUSTED_PROXIES` or turn
the exemption off. See [Settings](settings.md).

## Self-update

Docker Commander compares the running build with the latest **GitHub Release** and
shows admins an **"update available"** banner with a **View release** link. Its
close button hides it until a newer release comes out. The check runs server-side
and is cached. Set `DC_UPDATE_CHECK=0` to disable the outbound call on air-gapped
hosts.

**One-tap update (web UI).** An admin clicks **Update & restart** on the banner. It
downloads the release for your OS/arch, **verifies its SHA-256** (fail-closed: never
installs unverified code), atomically replaces the binary and restarts **in place**
(a re-exec, same PID, no supervisor needed). The UI reconnects on the new version.

- The binary's directory must be writable by the service user: the swap writes a
  temp file there and renames it over the binary.
- The [hardened systemd unit](../deploy/dockercmd.service) lists `/usr/local/bin`
  in `ReadWritePaths` for this reason. With `ProtectSystem=strict` and that
  directory *not* listed, the button fails with "read-only file system" even though
  the binary's permissions look fine. Remove it from `ReadWritePaths` if you'd
  rather require `sudo dockercmd --self-upgrade` outside the service.
- `DC_SELF_UPDATE=0` disables web-triggered updates (the banner still shows).
- Not offered on Windows: restart the service manually after updating.

**Auto-apply.** Admins can opt in under **Settings → Security**. Off by default.
When on, a granularity setting caps how far it may jump: patch only, patch+minor
(the default once enabled), or everything including major. It checks on the same
6-hour cadence as the banner, uses the same verified download-and-swap, and never
runs at the same time as a manual apply. Every automatic apply is audited
(`update.apply`, marked automatic), and each admin sees a one-time "you're now on
vX.Y.Z" notice at next login.

**From the CLI** (for scripted or headless upgrades):

```bash
dockercmd --self-upgrade           # download, verify SHA-256, replace in place
dockercmd --self-upgrade --check   # only report whether an update is waiting
```

It **verifies the SHA-256** and atomically replaces the binary, keeping its
permissions; **restart** the service afterwards. The binary must be writable by the
invoking user, and that is checked **before** the multi-MiB download. Without the
permission, in an interactive terminal it offers to re-exec elevated (`sudo` on
Linux/macOS, UAC on Windows) via an explicit `[y/N]` prompt. Installed from a
package manager? Update through that instead.

## Locked out

If the only admin's password is gone, reset it on the machine the instance runs on:

```bash
sudo dockercmd --data-dir /var/lib/dockercmd --reset-password admin   # packaged install
dockercmd --reset-password admin                                     # running it yourself
```

- **`--data-dir` matters on a packaged install.** The service gets its path from
  `-data-dir` in its systemd unit; standalone actions don't, so without it
  the command looks in *your* config directory. It refuses to create a database
  rather than answer "no such account" from an empty one.
- It prompts at the terminal; the password is never an argument, so it stays out
  of shell history and `/proc/<pid>/cmdline`. It ends **every browser session** of
  that account and writes the reset to the audit log.
- **No need to stop the service**, and no running server is needed. It writes to
  the data dir through SQLite, and the server re-reads the password and session
  epoch on every request: old sessions get `401` and the new password works at once.
- It does **not** touch the **second factor** (you'll still be asked for your code
  or passkey, unless the localhost 2FA exemption is on and you sign in from the
  machine itself), and does **not** revoke **API and MCP tokens**, which aren't
  sessions. After a suspected compromise, review those in the UI too.

Access to the data dir is its only authorisation. That is defensible: the session
signing secret is a row in that database (see [Backup & restore](#backup--restore)),
so anyone who can run this could already mint an admin session. Guard the data dir
accordingly.

## Backup & restore

A backup holds the SQLite database plus `projects/`, `project-templates/` and
`project-revisions/` (the file snapshot of every deploy revision) from the **data
dir**. Both secret keys, the session signing secret and the at-rest encryption key,
are rows *inside the database*. A backup is therefore self-contained and restores
onto a fresh machine as-is.

Nothing else in the data dir goes in. Left out on purpose: `tls/` (from
`--make-certs`, so run it again) and the ACME caches `acme/` and `proxy-acme/`. On a
new machine ACME therefore requests fresh certificates, which counts against the
CA's rate limits.

```bash
dockercmd --backup /var/backups/dc-$(date +%F).tar.gz               # plain
dockercmd --backup /var/backups/dc.tar.gz --passphrase              # encrypted (prompts)
echo "$PASS" | dockercmd --backup /var/backups/dc.tar.gz --passphrase   # for cron
```

Like `--reset-password`, these don't read the config file. On a packaged install,
add `--data-dir /var/lib/dockercmd`. If the data dir holds no database, or a file
that isn't a Docker Commander database, `--backup` refuses and says so, rather
than backing up an empty one. It opens the database read-only and never changes
it.

The backup is taken through a live database connection (`VACUUM INTO`), so it is
**safe while the server runs**. Copying the `.db` file yourself is not: the
database runs in WAL mode and committed data can still sit in the `-wal` file.

> **A backup is equivalent to every secret you have stored.** The encryption key
> travels inside the database, so the archive effectively holds the plaintext of
> host TLS keys, SMTP and LDAP passwords and registry credentials. The file is
> written mode `0600`. Use `--passphrase` (AES-256-GCM, Argon2id) if it leaves the
> machine. The passphrase is read from the terminal or stdin, never the command
> line, so it stays out of shell history and `/proc/<pid>/cmdline`.

Restoring replaces the data dir, so **stop the server first**:

```bash
systemctl stop dockercmd
dockercmd --restore /var/backups/dc.tar.gz            # refuses if a DB is present
dockercmd --restore /var/backups/dc.tar.gz --force    # overwrite an existing install
systemctl start dockercmd
```

Nothing enforces the stop: restoring under a live process leaves it holding a
database that no longer exists. Without `--force`, restore won't overwrite an
existing installation, so a mistyped path can't destroy one. Archive entries are
jailed to the data dir, and a **symlink** entry is refused outright. A restore
also won't write through a symlink that is already in the data dir and points
out of it. With `projects/` linked to another disk, restore into a fresh data
dir and move the folders over afterwards.

**Symbolic links are not backed up, and the backup says so.** If something in the
data dir is a link (`projects/` on a bigger disk, say), neither the link nor what
it points to goes into the archive, and `--backup` prints the skipped paths.
**Back those up yourself, or use a bind mount instead of a symlink.** Hard links
are different: a hard link *is* the file, so its data is included.
