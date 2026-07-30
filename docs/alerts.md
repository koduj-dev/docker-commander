# Alerts


> The **SMTP server** moved to [Settings → Email](settings.md#email-smtp) and is
> now admin-only — it is one relay for the whole installation. Alert rules still
> opt into e-mail here.
[← Manual index](README.md)

![Alerts](images/alerts.png)

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

> **Host reachability is watched automatically** — no rule needed. When a host's
> Docker daemon goes **unreachable** you get a *critical* `host` alert, and a
> *recover* (*info*) when it comes back. See
> [Hosts → Reachability monitoring](hosts.md#reachability-monitoring).

### Import / export
**Export** downloads every rule as a portable JSON bundle (`alert-rules.json`)
you can keep in version control or move to another instance. **Import** reads such
a bundle and creates the rules it contains — it never overwrites or deletes
existing rules, and every rule is re-validated on the way in.

Webhooks are referenced **by name**, and a webhook's URL/headers/secrets are
never part of the bundle. On import a rule is re-linked to a local webhook only
if one with the same name already exists; otherwise it's imported without a
destination and the skipped link is reported. Recreate any missing webhooks (in
the **Webhooks** tab) before or after importing, then edit the rule to attach it.

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

## Who receives an alert e-mail
A rule that has **Also send an email** ticked resolves its recipients in this
order, most specific first:

1. **The rule's own recipients** — the comma-separated list on the rule.
2. **The host's alert address** — set per host under [Hosts](hosts.md), for a host
   whose alerts should go elsewhere.
3. **The instance-wide recipient** — the *To* field under
   [Settings → Email](settings.md#email-smtp).

Rules created before per-rule recipients existed have an empty list, so they keep
using 2 or 3 exactly as before.

Set an **alert e-mail on your account** (the icon beside *Sign out*) and it
prefills as the recipient the first time you enable e-mail on a rule — clear the
field to fall back to the instance-wide address instead. If your LDAP directory
publishes a `mail` attribute, it is filled in for you on login; a directory with no
address never clears one you set by hand.
