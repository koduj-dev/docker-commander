# Alerts

[← Manual index](README.md)

The alert engine runs on the **server**, watching **all configured hosts** 24/7
— it keeps working whether or not anyone has the UI open.

## Tabs
- **Feed** — fired alerts (time, severity, host, container, message). Acknowledge
  to clear the unread badge.
- **Rules** — define and edit what fires (below).
- **Webhooks** — HTTP destinations.
- **Email** — the SMTP server.

## Rules
Create or edit a rule (rules are fully editable, not just create/delete):

| Type       | Fires when… |
|------------|-------------|
| `state`    | a container emits a lifecycle event (die, kill, oom, stop, unhealthy) |
| `resource` | CPU% or MEM% crosses a threshold for *N* seconds |
| `log`      | a log line matches a substring or regex |
| `restart`  | a container restarts too often within a window (crash loop) |

Each rule has a **target** (container-name substring; blank/`*` = all), a
**severity**, a **cooldown** (suppresses repeats — keep it generous, e.g. 60s),
and optional **webhook** and **email** delivery.

## Webhooks
Fire to any HTTP endpoint (Slack, Discord, Grafana, n8n…). The body is a Go
template over `{{.RuleName}} {{.Severity}} {{.Type}} {{.Container}} {{.Message}}
{{.Value}} {{.Time}}`; with no template the alert is sent as JSON.

## Email (SMTP)
Configure host/port, optional username + password (encrypted at rest), from and
to, and TLS (implicit or STARTTLS). **Send test** verifies it. Per-host routing:
a host's *alert email* (set on the [Hosts](hosts.md) page) overrides the global
recipient for alerts from that host.

## Prometheus
Scrape `/metrics` for `dockercmd_container_cpu_percent`, `_mem_bytes`,
`_mem_percent` and `_container_running`, labelled by `id`, `name` and `host`.
Protect it with `DC_METRICS_TOKEN` if exposed.

## System log
Beyond these channels, every fired alert is also written to the process log
(stderr) as a structured line, so under systemd it lands in the journal — and,
if you enable forwarding, in syslog. See
[Deployment → Logs](deployment.md#logs).
