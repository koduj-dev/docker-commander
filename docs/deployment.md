# Deployment

[← Manual index](README.md)

Docker Commander is a single binary that embeds the UI. The server runs
monitoring, alerting and metric history **continuously** — independent of any
connected browser — so on a server you'll want it supervised.

## Configuration
Every option is a flag with a `DC_*` environment-variable equivalent. See
[`.env.example`](../.env.example) for the full list. Key ones:

| Env | Default | Purpose |
|-----|---------|---------|
| `DC_HOST` | `127.0.0.1` | listen host/interface (use `0.0.0.0` for all; keep on loopback behind a proxy) |
| `DC_PORT` | `8080` | listen port (also `-p 9000` shorthand) |
| `DC_ADDR` | (unset) | legacy full `host:port`; overrides `DC_HOST`/`DC_PORT` if set |
| `DC_DATA_DIR` | OS config dir | SQLite DB + signing/encryption keys |
| `DC_METRICS_TOKEN` | (open) | bearer token guarding `/metrics` |
| `DC_REDIS_ADDR` | (memory) | Redis for metric history |
| `DC_METRICS_RETENTION` | `6h` | history retention |

Docker connection honours `DOCKER_HOST` / `DOCKER_CERT_PATH`.

### Config file
When running as a service, the simplest place for settings is a config file. It
is a plain `KEY=VALUE` file using the same `DC_*` keys; `#` starts a comment and
`export `/quotes are tolerated. (Flags and env vars still work and take
precedence, but the config file is the recommended single source of truth.)

```ini
# /etc/docker-commander/commander.conf
DC_HOST=127.0.0.1
DC_PORT=8080
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

## Reverse proxy + TLS
Bind to loopback and terminate TLS at nginx/Caddy. WebSockets must be allowed
(stats, logs, exec, events) — proxy `Upgrade`/`Connection` headers. Example
(nginx) for a location:

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host;
}
```

> Behind a proxy, **keep the localhost 2FA exemption off** — it trusts the
> connection address, which becomes the proxy. See [Settings](settings.md).

## Backup
Back up `DC_DATA_DIR` — it holds the SQLite database and the keys that sign
sessions and encrypt stored secrets. Losing the keys means re-entering registry
/ SMTP / LDAP passwords.
