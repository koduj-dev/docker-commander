# Deployment

[← Manual index](README.md)

Docker Commander is a single binary that embeds the UI. The server runs
monitoring, alerting and metric history **continuously** — independent of any
connected browser — so on a server you'll want it supervised.

## Configuration
Nearly every option is a flag with a `DC_*` environment-variable equivalent, and
can live in a config file. Two exceptions: **`-session-ttl`** (how long a signed-in
session lasts, default **12h**) is flag-only, and `DC_REDIS_DB` is
environment-only. See
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
| `DC_REDIS_ADDR` | (memory) | Redis for metric history; empty keeps the in-memory ring buffer |
| `DC_REDIS_PASSWORD` | (empty) | Redis password, if the server requires auth |
| `DC_REDIS_DB` | `0` | Redis database index |
| `DC_METRICS_RETENTION` | `6h` | history retention |
| `DC_METRICS_INTERVAL` | `15s` | how often the monitor samples every running container's stats — **raise it** (e.g. `30s`/`60s`) on a host with many containers if the sampling sweep is costly |
| `DC_TRUSTED_PROXIES` | (none) | comma-separated reverse-proxy IPs/CIDRs whose `X-Forwarded-For` is trusted for the real client IP — **set this when behind a proxy** (see below) |
| `DC_UPDATE_CHECK` | `1` | check GitHub Releases for a newer version (admin banner); set `0` to disable the outbound call |
| `DC_SELF_UPDATE` | `1` | allow admins to apply an update from the web UI (the one-tap "Update & restart"); set `0` to keep the banner but forbid web-triggered self-replacement |
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

### The binary installs itself (Linux / macOS / Windows)
The simplest path — the binary writes the service definition for the current OS,
installs itself to a stable location, and starts it. No script, no manual steps:

```bash
sudo ./dockercmd --install-service     # Linux    — systemd (needs root)
./dockercmd --install-service          # macOS    — launchd LaunchAgent (your user, NOT sudo)
dockercmd.exe --install-service        # Windows  — SCM service (elevated PowerShell/cmd)

dockercmd --service-status             # show service status
sudo dockercmd --uninstall-service     # stop + remove (keeps the data dir)
```

On **Linux** it creates the dedicated `dockercmd` user in the `docker` group,
copies itself to `/usr/local/bin/dockercmd`, installs the hardened unit and
`enable --now`s it. On **macOS** it installs a per-user LaunchAgent under
`~/Library` (no sudo — a system daemon can't reach Docker Desktop's user-owned
socket). On **Windows** it copies itself to
`%ProgramFiles%\docker-commander\dockercmd.exe`, registers a real Service
Control Manager (SCM) service with auto-restart on failure, and starts it —
see [Windows (native service)](#windows-native-service-or-scheduled-task)
below. Uninstall leaves the data dir (and, on Linux, the service user) in
place so reinstalling keeps the database and keys.

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

### Installer scripts (alternative)
Equivalent idempotent installers also live in [`deploy/`](../deploy/) — handy to
read exactly what gets installed, or on Windows if you'd rather use a
Scheduled Task than the native SCM service:

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

### Windows (native service, or Scheduled Task)
`dockercmd.exe --install-service` registers a real **Service Control Manager
(SCM)** service: it copies itself to
`%ProgramFiles%\docker-commander\dockercmd.exe`, creates the service (start
type Automatic, delayed auto-start, with SCM recovery actions to restart on
crash) and starts it. This needs an elevated (Administrator) prompt. Logs go to
the **Event Viewer** (Windows Logs → Application, source `dockercmd`); data
lives under `%ProgramData%\docker-commander\data`.

`install-windows.ps1` remains as a dependency-free alternative: it registers a
**Scheduled Task** instead, which starts at boot (or `-AtLogon`, if Docker
Desktop only runs under your account) and restarts on failure. Useful if you'd
rather not run as SYSTEM, or want to read exactly what gets installed without
touching the SCM. Wrapping the exe with [NSSM](https://nssm.cc) or WinSW is
also still an option, and no longer necessary just to get a "real" service.

Pick **one** of the two — running both at once means two copies of dockercmd
racing over the same data dir and port. Each installer refuses to proceed if
it detects the other is already installed (checks for the SCM service `dockercmd`
or the Scheduled Task `DockerCommander` by name); migrate by stopping and
removing the one you're leaving before installing the other.

The data dir's ACL is set explicitly on **every startup**, not just install —
`SYSTEM` and `Administrators` get Full Control, nothing else (not the
inherited `%ProgramData%` default, which can otherwise leave it readable by
any local account) — since it holds the database, TLS private keys, and the
at-rest encryption key. This applies the same way whether dockercmd is
running as the native SCM service, under the Scheduled Task installer, or
just in a console for testing. `--install-service` additionally checks an
*existing* data dir before reinstalling over it: if its permissions already
grant access beyond `SYSTEM`/`Administrators`/`CREATOR OWNER`, install refuses
to proceed rather than silently trusting and "fixing" it — inspect it by hand
first.

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

For a quick **self-signed** cert (LAN / internal use) without `openssl`, run
`dockercmd --make-certs [hostnames…]`: it writes `cert.pem` + `key.pem` (key mode
0600) into `<data-dir>/tls/`, covering localhost plus any hosts you list, and
prints the `DC_TLS_CERT` / `DC_TLS_KEY` to set. Clients warn until they trust it.

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
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
}
```

**Set those last two headers.** `$proxy_add_x_forwarded_for` appends the real peer
to whatever the client sent — without it nginx forwards the client's own
`X-Forwarded-For` untouched, so anyone can claim to be any address, and the client
IP keys your rate limits and audit records. `X-Forwarded-Proto` is what tells the
app the connection was HTTPS, which is what marks the session cookie `Secure`.
Both are believed **only** from an address listed in `DC_TRUSTED_PROXIES`, so set
that too.

> The **localhost 2FA exemption never applies to a proxied request**, whatever
> address it resolves to — a proxy cannot vouch that someone is sitting at the
> machine. You can leave the setting on for local use without it leaking through
> the proxy. See [Settings](settings.md).

## Self-update
Docker Commander compares the running build against the latest **GitHub
Release** and shows an admin **"update available"** banner when a newer version
exists. The check is cached and runs server-side; set `DC_UPDATE_CHECK=0` to
disable the outbound call on air-gapped hosts.

**One-tap update (web UI).** When an update is available, an admin can click
**Update & restart** on the banner. It downloads the release for your OS/arch,
**verifies its SHA-256** (fail-closed — never installs unverified code),
atomically replaces the binary and restarts the process **in place** (a re-exec,
same PID — no supervisor required), then the UI reconnects on the new version.
The binary must be writable by the service user. Disable web-triggered updates
with `DC_SELF_UPDATE=0` (the banner still shows). Not offered on Windows —
restart the service manually after updating.

**From the CLI** (equivalent, for scripted or headless upgrades):

```bash
dockercmd --self-upgrade           # download, verify SHA-256, replace in place
dockercmd --self-upgrade --check   # only report whether an update is waiting
```

`--self-upgrade` fetches the release asset for your OS/arch, **verifies its
SHA-256**, and atomically replaces the running binary (preserving its
permissions). The binary must be writable by the invoking user; **restart** the
service afterwards to run the new version. (Installed from a package manager?
Update through that instead.)

## Locked out

If the password for the only admin account is gone, reset it from the machine the
instance runs on:

```bash
sudo dockercmd --data-dir /var/lib/dockercmd --reset-password admin   # packaged install
dockercmd --reset-password admin                                     # running it yourself
```

`--data-dir` matters on a packaged install: the service reads its path from
`/etc/docker-commander/commander.conf`, and standalone actions do not, so without
it the command looks in *your* config directory. It refuses to create a database
rather than answering "no such account" from an empty one.

It prompts at the terminal — the password is never an argument, so it stays out of
shell history and `/proc/<pid>/cmdline` — ends **every browser session** for that account,
and writes the reset to the audit log.

Two things it deliberately does *not* do. The **second factor is not touched** — you
will still be asked for your code or passkey afterwards, unless this instance has
the localhost 2FA exemption on and you sign in from the machine itself. And **API
and MCP tokens are not revoked**: they are not sessions. If you are resetting
because of a suspected compromise, review those in the UI as well.

**You do not have to stop the service.** It writes through SQLite the same way the
server does, and the server re-reads both the password and the session epoch on
every request — so the old sessions stop working and the new password starts
working immediately, with no restart. (Verified: an active session answers 401 the
moment the reset lands.)

It needs no server either; it works directly on the data dir, and `--data-dir`
applies as usual. That access is the only authorisation it has, which is defensible for the
same reason the warning under *Backup & restore* is true: the session signing
secret is a row inside that database, so anyone who can run this could already
mint themselves an admin session. Guard the data dir accordingly.

## Backup & restore

Everything the installation needs lives under the **data dir**: the SQLite
database plus `projects/` and `project-templates/`. Both secret keys — the session
signing secret and the at-rest encryption key — are rows *inside that database*, so
a backup is self-contained and restores onto a fresh machine as-is.

```bash
dockercmd --backup /var/backups/dc-$(date +%F).tar.gz               # plain
dockercmd --backup /var/backups/dc.tar.gz --passphrase              # encrypted (prompts)
echo "$PASS" | dockercmd --backup /var/backups/dc.tar.gz --passphrase   # for cron
```

The backup is taken through a live database connection (`VACUUM INTO`), so it is
**safe while the server is running** — copying the `.db` file yourself is not, as
it runs in WAL mode and committed data can still sit in the `-wal` file.

> ⚠️ **A backup is equivalent to every secret you have stored.** Because the
> encryption key travels inside the database, the archive effectively contains the
> plaintext of host TLS keys, the SMTP and LDAP passwords and registry credentials.
> The file is written mode `0600`; use `--passphrase` (AES-256-GCM, Argon2id) if it
> leaves the machine. The passphrase is read from the terminal or stdin, never from
> the command line, so it stays out of shell history and `/proc/<pid>/cmdline`.

Restoring replaces the data dir, so **stop the server first**:

```bash
systemctl stop dockercmd
dockercmd --restore /var/backups/dc.tar.gz            # refuses if a DB is present
dockercmd --restore /var/backups/dc.tar.gz --force    # overwrite an existing install
systemctl start dockercmd
```

**Stop the server first** — as in the snippet above. The database is replaced
wholesale, and nothing enforces this: restoring underneath a live process leaves
it holding a database that no longer exists. Restore also refuses to overwrite an
existing installation unless `--force`, so a mistyped path can't destroy an
instance by accident. Archive entries are jailed to the data dir, so a tampered
backup can't write elsewhere on the filesystem, and a **symlink** entry is
refused outright rather than inspected — see below.

> **Symbolic links are not backed up — and the backup says so.** If something in
> the data dir is a link (`projects/` pointed at a bigger disk, say), neither the
> link nor anything behind it goes into the archive, and `--backup` prints the
> paths it skipped. That was always true of the contents — the backup never
> followed links — but it used to happen silently, which is the worst version of
> it: a backup that looks complete and isn't. **Back those paths up yourself, or
> use a bind mount instead of a symlink.**
>
> Restoring an archive that contains a symlink entry is refused outright. Hard
> links are a different thing: a hard link *is* the file, so its data is included
> like any other file's.
