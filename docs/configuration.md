# Configuration

[← Manual index](README.md)

Every setting the server reads. For what they mean in a real deployment
(HTTPS, reverse proxies, packages), see [Deployment](deployment.md).

Options are flags with an environment-variable equivalent, and can also live in
a config file — see [`deploy/commander.conf.example`](../deploy/commander.conf.example).
Two exceptions: `-session-ttl` has no variable, and `DC_REDIS_DB` has no flag.
On/off variables are on only for exactly `1` (`DC_DEV`, `DC_MCP_ENABLED`,
`DC_PROXY_ENABLED`, `DC_PPROF`), or off only for exactly `0` (`DC_UPDATE_CHECK`,
`DC_SELF_UPDATE`). The Docker connection also honours the standard `DOCKER_HOST` /
`DOCKER_CERT_PATH` variables.

| Flag                 | Env                    | Default            | Description |
|----------------------|------------------------|--------------------|-------------|
| `-host`              | `DC_HOST`              | `127.0.0.1`        | Listen host/interface. Use `0.0.0.0` to bind all (deliberate). |
| `-port` / `-p`       | `DC_PORT`              | `8470`             | Listen port. |
| `-addr`              | `DC_ADDR`              | (unset)            | Legacy full `host:port`; overrides `-host`/`-port`. |
| `-tls-cert`          | `DC_TLS_CERT`          | (off)              | PEM certificate path; with `-tls-key`, serves **HTTPS** directly. |
| `-tls-key`           | `DC_TLS_KEY`           | (off)              | PEM private-key path. |
| `-acme-domains`      | `DC_ACME_DOMAINS`      | (off)              | Comma-separated public hostname(s): automatic **HTTPS** via ACME/Let's Encrypt instead of a static cert. Mutually exclusive with `-tls-cert`/`-tls-key`. |
| `-acme-email`        | `DC_ACME_EMAIL`        | (unset)            | Contact email registered with the ACME account (optional). |
| `-acme-cache-dir`    | `DC_ACME_CACHE_DIR`    | `<data-dir>/acme`  | Where issued ACME certificate/account state is cached between restarts. |
| `-acme-directory-url`| `DC_ACME_DIRECTORY_URL`| Let's Encrypt prod | Override the ACME directory — e.g. the staging directory. **Not** a local Pebble instance, which this server cannot obtain a certificate through — see [Deployment](deployment.md#https). |
| `-mcp-enabled`       | `DC_MCP_ENABLED=1`     | off                | Enable the remote **MCP** server for AI tools. Off by default; serve behind HTTPS. See [MCP](mcp.md). |
| `-mcp-public-url`    | `DC_MCP_PUBLIC_URL`    | (unset)            | Externally reachable base URL (`https://host`) — required for the MCP **OAuth** flow (bearer tokens work without it). |
| `-proxy-enabled`     | `DC_PROXY_ENABLED=1`   | off                | Embedded reverse proxy for projects' domain mappings, on port 443. Needs ACME mode. See [Projects](projects.md#domains). |
| `-proxy-acme-cache-dir` | `DC_PROXY_ACME_CACHE_DIR` | `<data-dir>/proxy-acme` | Where the proxy's own ACME certificates are cached. |
| `-config`            | `DC_CONFIG`            | `/etc/docker-commander/commander.conf` | Config file path. On Windows `%ProgramData%\docker-commander\commander.conf`. |
| `-data-dir`          | `DC_DATA_DIR`          | OS config dir      | SQLite DB + signing/encryption keys. |
| `-session-ttl`       | —                      | `12h`              | Session token lifetime. |
| `-dev`               | `DC_DEV=1`             | off                | Dev mode: API only + permissive CORS for Vite. |
| `-metrics-token`     | `DC_METRICS_TOKEN`     | (open)             | If set, `/metrics` needs `Authorization: Bearer <token>` (or `?token=`). |
| `-redis-addr`        | `DC_REDIS_ADDR`        | (memory)           | Redis `host:port` for metric history; empty = in-memory ring. |
| `-redis-password`    | `DC_REDIS_PASSWORD`    | (empty)            | Redis password; `DC_REDIS_DB` selects the DB index. |
| `-metrics-retention` | `DC_METRICS_RETENTION` | `6h`               | History retention (e.g. `30m`, `24h`). |
| `-metrics-interval`  | `DC_METRICS_INTERVAL`  | `15s`              | How often container stats are sampled; raise it on hosts with many containers. |
| `-deploy-silence-grace` | `DC_DEPLOY_SILENCE_GRACE` | `3m`         | Hold back a project's alert deliveries this long after a successful deploy; `0` disables. |
| `-log-file`          | `DC_LOG_FILE`          | (stderr)           | Write the log to this file instead, rotated at 10 MiB with one older copy (`<file>.1`). |
| `-pprof`             | `DC_PPROF=1`           | off                | Go profiling on a dedicated loopback listener, `127.0.0.1:6060`. |
| `-update-check`      | `DC_UPDATE_CHECK`      | on                 | Check GitHub for newer releases; `0` disables. |
| `-trusted-proxies`   | `DC_TRUSTED_PROXIES`   | (none)             | Reverse-proxy IPs/CIDRs whose `X-Forwarded-For` may be trusted. The client IP keys rate limits, the localhost 2FA exemption and audit records — **set it behind a proxy**, and never to a range you don't control. |
| `-self-update`       | `DC_SELF_UPDATE`       | on                 | Let admins apply an update from the web UI. `0` keeps the "update available" banner but forbids web-triggered self-replacement. |
