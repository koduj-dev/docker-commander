# Settings

_Admin only._ Tabs: Features, Security, Policy rules, LDAP, Email, MCP Admin, Data
retention and Recovery bundle. The tab is in the URL (for example
`/settings?tab=retention`), so a link or a reload opens the same tab.

[← Manual index](README.md)

![Settings](images/settings.png)

_Admin only._ App-wide configuration.

## Feature flags (enabled features)
Turn whole menu sections **on/off for everyone**. A disabled section is hidden
from the menu and its API is blocked — useful to trim the app to what your team
actually uses. Admins re-enable them here.

## Security — localhost 2FA exemption
By default **2FA is mandatory** for all logins. Enable this to let connections
from **loopback** (`127.0.0.1` / `::1`) log in with a password only (skipping
both the enrollment gate and the TOTP challenge). Remote connections always
require 2FA.

- It applies only to a **direct** loopback connection. A request that arrived
  through a reverse proxy never qualifies, even when it resolves to `127.0.0.1`:
  a proxy on the same machine is itself loopback, and a client's forwarded header
  is only a claim. So the exemption cannot leak through a proxy — but it also
  cannot be used *by* one, which is the point.
- Good for a personal/local install; leave off for shared servers.
- This is the same toggle the **first-run setup** screen flips when you choose
  *"Skip 2FA for now"* — so you can decide up front and change it here later.

## Security — MCP token lifetime

![Settings → Security](images/settings_security.png)

Instance-wide rules for the API tokens users mint on the [MCP](mcp.md) page. Three
settings, all admin-only:

- **Default lifetime** — **30 days** out of the box. Pre-selected in the creation
  form, and used when a client asks for no particular expiry.
- **Maximum lifetime** — a ceiling, **365 days** by default. Without one, "no
  never-expiring tokens" is a formality anyone sidesteps by asking for 99999 days.
- **Allow never-expiring tokens** — **off**. Turn it on only for an integration
  that genuinely needs a permanent credential; the ceiling can then be cleared.

Revocation already existed, but it needs somebody to remember — and the tokens
most worth revoking are the ones everyone has forgotten. An expiry date is the
only control here that keeps working when nobody is paying attention.

The policy governs what may be **minted**, not what exists: tokens keep the expiry
they were given, so tightening it will not cut a running integration off
overnight. To retire tokens that predate a stricter rule, revoke them on the **MCP
Admin** page, which stays the operational view of who holds what. The creation
form only offers lifetimes the server will accept, and the server re-checks anyway
— a form is not a boundary.

Contradictory settings are repaired rather than stored: a default above the
ceiling is lowered to it, and clearing the ceiling while never-expiring tokens are
off puts the 365-day one back. The page shows what is actually in force after
saving, not what was typed.

## Security — self-update auto-apply

Self-update itself (see [Deployment](deployment.md#self-update)) already lets
an admin apply a new release by hand from the update banner. This setting
lets it happen automatically instead:

- **Off by default.** Nothing changes until an admin opts in.
- **Granularity** caps how far an automatic apply is allowed to jump: **patch
  only**, **patch & minor** (the sensible default once enabled — a
  WordPress-style choice, not "auto-apply everything"), or **everything,
  including major**. It's a ceiling, not an exact match — "patch & minor"
  also lets a patch release through.

It checks on the same cadence as the manual update banner, downloads and
verifies the release the same way the one-tap **Update & restart** button
does, and never runs at the same time as a manual apply. The granularity
ceiling is enforced against the exact release resolved at the moment of
install, never an earlier cached check, so it can't be bypassed by a release
that ships between two checks. Every automatic apply is recorded in the
audit log, and every admin sees a one-time "you're now on vX.Y.Z — applied
automatically" notice at their next login, until they dismiss it.

The control is disabled, with an explanation, when auto-apply could never
actually run: the update check itself is off (`DC_UPDATE_CHECK=0`), or
self-update is unavailable (`DC_SELF_UPDATE=0`, or a platform that can't
restart itself in place, e.g. Windows) — the same capability the one-tap
button already keys off, so a saved policy is never silently dormant.

## LDAP / Active Directory

![Settings → LDAP](images/settings_ldap.png)

Optional external authentication.

- **Enable** + **Server URL** (`ldap://host:389` or `ldaps://host:636`),
  optional **StartTLS**.
- **Bind DN** + **password** — a service account used to search (encrypted at
  rest); leave password blank to keep the stored one.
- **User base DN** and **User filter** (must contain `%s`, e.g. `(uid=%s)` or
  `(sAMAccountName=%s)`).
- **Admin group DN** (optional) — members are provisioned as admins.
- **Group mappings** (optional) — grant access by LDAP group membership: add a
  mapping (a group DN + the **roles** its members get, and/or raw sections). A
  user's access is the **union** across every mapping whose group they belong to.
  Prefer roles; the section pills predate them and remain for older configs.
- **Test** verifies dial / bind / search.

**How login works:** local accounts always use their local password. A username
with no local account (while LDAP is enabled) is authenticated against the
directory and **provisioned as a local `user`** (or `admin` if in the admin
group). Such users can still enroll their own TOTP.

**Sections:** without group mappings, you grant an LDAP user's sections manually
in [Users](users.md), and they persist. **Once any group → section mapping is
configured, LDAP becomes authoritative** for non-admin users' sections: they're
recomputed from current group membership on **every login** (so adding/removing a
user from a group takes effect on their next sign-in, and manual section edits
are overwritten). Group DNs are matched on the full DN, case-insensitively;
unknown section names are ignored. The DN must match the form your directory
returns in `memberOf` (DNs aren't canonicalised, so avoid stray inter-RDN
spaces); if a mapping never applies, check the exact DN with **Test** or your
directory tooling. The match fails closed — a mismatch only ever denies.

**Roles** follow the same matching but a different switch: they're re-synced from
the directory only once **at least one mapping actually grants a role**, so
upgrading doesn't strip roles from installs whose mappings predate them. See
[Roles from LDAP groups](users.md) for the full table. A mapping can never grant
admin — only the admin group DN does that — and a role id left behind by a deleted
role grants nothing rather than failing the login.

**Fallback role** (optional, shown once a mapping grants a role) — granted in place
of a mapped role that no longer exists, so deleting a role degrades its members to a
baseline instead of leaving them with nothing. It does not apply to users whose
groups map to no role at all, and the nominated role can't be deleted while it's the
fallback.

The admin role stays "sticky" once granted —
removing someone from the admin group does not auto-demote them (avoids lockout
if the directory is unreachable); demote them in [Users](users.md).

## Email (SMTP)

![Settings → Email](images/settings_email.png)

One outbound mail relay for the **whole installation** — used by alert rules that
opt into e-mail, and by system notifications. Set host/port, optional credentials,
and the From / To addresses (To takes a comma-separated list), then **Send test**
to check it end to end. The password is encrypted at rest and never returned by
the API.

**Transport security** is one checkbox, not a choice of two: tick **Implicit TLS**
for a relay that expects TLS from the first byte (port 465). Leave it off and the
connection starts in the clear and is upgraded with **STARTTLS if the server
offers it** — the usual arrangement on port 587. That upgrade is opportunistic, so
a relay that advertises no STARTTLS is talked to in plaintext rather than refused;
on an untrusted network, use implicit TLS.

> **Admin only.** This used to live under [Alerts → Email](alerts.md) and was
> reachable by anyone with the *alerts* section. Because it is a single
> instance-wide relay, that let a non-admin repoint the installation's mail — so
> it moved here. Managing alert rules, webhooks and the feed still only needs the
> *alerts* section; only the relay itself now needs an admin.

## Policy rules
![Policy rules](images/settings_policy.png)

Checks that run on every project deploy (privileged container, host network, Docker
socket mount, `:latest` image, no limits, no healthcheck). Each rule is Off, Warn
or Block. Details in [Policy rules](policy-rules.md).

## MCP Admin
All API tokens, OAuth clients and sessions of all users, with revoke. See
[MCP → Admin overview](mcp.md#admin-overview-the-mcp-admin-page).

## Data retention
![Data retention](images/settings_retention.png)

Old history is deleted once a day. This tab sets how long each kind is kept. It is
on by default:

| Area | Default | Notes |
| --- | --- | --- |
| Alert events | 90 days | The [Alerts feed](alerts.md#the-feed). |
| Alert deliveries | 90 days | Webhook and e-mail attempts. They are deleted with their event, so this can't be longer than the events. |
| Audit log | 365 days | 30 days at the minimum. |
| Project revisions | newest 50 per project | A count, not an age. The snapshot file of a deleted revision is removed too. At least 3. |

Set a row to **keep forever** to never delete it. **Reset to defaults** fills in the
form without saving.

Next to each row you see how many entries are stored and the age of the oldest one.
At the top is the database size. SQLite does not shrink the file after a delete, it
reuses the free space, so the size stays the same.

- **Schedule.** The first purge runs about two minutes after the server starts,
  then every 24 hours. It deletes in small batches. Snapshot files that have no
  revision left (from a failed purge) are removed on the next run.
- **Purge now** runs the purge immediately, after a confirmation. It uses the saved
  policy, so it is disabled while the form has unsaved changes.
- **Last purge.** The page shows when it ran, whether it was scheduled or manual,
  what it deleted, how long it took, the database size before and after, and any
  error. Each run writes a line to the process log. If it deleted something or
  failed, it also adds a `retention.purge` entry to the [audit log](audit.md).
- **Unreadable policy.** If the saved policy can't be read, nothing is deleted. The
  page shows a warning and the defaults; save a policy to resume.

> **Upgrading to 1.7.0:** the defaults are on, so the first purge deletes alert
> history older than 90 days and audit entries older than a year. To keep more,
> change the values here before the server has been up for a few minutes.

Limits are listed in [Limits](limits.md#history-retention).

## Recovery bundle
![Recovery bundle](images/settings_recovery.png)

Export your setup to one file and import it on another instance. See
[Recovery bundle](recovery.md).
