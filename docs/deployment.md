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
| `DC_DATA_DIR` | OS config dir | SQLite DB + signing/encryption keys |
| `DC_METRICS_TOKEN` | (open) | bearer token guarding `/metrics` |
| `DC_REDIS_ADDR` | (memory) | Redis for metric history |
| `DC_METRICS_RETENTION` | `6h` | history retention |
| `DC_UPDATE_CHECK` | `1` | check GitHub Releases for a newer version (admin banner); set `0` to disable the outbound call |

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

## systemd (Linux)
A hardened unit and a config example live in [`deploy/`](../deploy/).

```bash
sudo install -m755 dockercmd /usr/local/bin/dockercmd
sudo useradd --system --no-create-home --shell /usr/sbin/nologin dockercmd
sudo usermod -aG docker dockercmd
sudo install -d /etc/docker-commander && sudo cp deploy/commander.conf.example /etc/docker-commander/commander.conf   # edit
sudo cp deploy/dockercmd.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now dockercmd
```

The unit runs as a dedicated user in the `docker` group with
`NoNewPrivileges`, `ProtectSystem=strict` and a private `StateDirectory`.

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
