# Users & roles

[← Manual index](README.md)

_Admin only._ Manage accounts and what each can do.

![Users & roles](images/users.png)

## Account types
- **admin** — full access plus administration (users, roles, settings, all hosts).
- **user** — limited to what you grant, and optionally **read-only** for the whole
  account (can view, but every mutating action — start/stop, exec, upload, delete,
  create… — is blocked).

## Named roles
A **role** is a reusable bundle of section grants, so you don't tick thirteen
checkboxes per account. Each section in a role is either **read-only** or
**writable**, which is finer-grained than the account-level read-only flag.

Two roles ship built in and cannot be edited — **Duplicate** one to make an
editable copy, the same way [project templates](projects.md#managing-templates)
work:

| Role | Grants |
|---|---|
| **Viewer** | Every section, read-only. |
| **Operator** | Day-to-day work — containers, projects, images, volumes, networks, topology, logs, events, alerts — writable. Deliberately **not** hosts, registries or the audit log, which are authority over the installation itself. |

A user can hold several roles, and can still have per-account sections on top.
Their **effective access** is the union, so the more permissive grant wins.

> Only an **admin** can create, edit or assign roles. Anyone able to edit a role
> could widen their own access, so no combination of section grants reaches role
> management.

## Managing accounts
- **New user** — username, password (min 10 chars), account type, read-only flag,
  any **roles**, and optionally per-account **sections** (checkboxes matching the
  menu).
- **Edit access** — change type / read-only / roles / sections later. Changes take
  effect on the user's **next request** — nothing is cached in their session, so
  revoking a role is immediate.
- **Reset password** — set a new password.
- **Delete** — with guards: you can't delete your own account or the last admin,
  and you can't demote the last admin.

## How enforcement works
Permissions are checked on the server for every request: the path maps to a
section, and a non-admin must have that section granted — with **write** access
for mutating calls. The menu also hides what you can't reach. Globally
[disabled sections](settings.md) are hidden and blocked for everyone.

The order the rules apply in, which matters when they disagree:

1. **admin** bypasses section and read-only checks.
2. Grants are the **union** of the account's roles and its own section list.
3. The account-level **read-only flag caps everything** to reads — a writable role
   cannot lift it.
4. An app-wide **disabled section** is removed last, so a role can never re-enable
   a feature an admin turned off.

> LDAP users are provisioned here automatically on first login (as `user`, or
> `admin` if in the configured admin group) — then you grant their sections like
> any other account. See [Settings → LDAP](settings.md).

## Note on the live stream
RBAC is enforced on the REST API **and** on the shared live stats/logs
WebSocket (`/api/ws`): each subscription is authorised per channel, and both the
**stats** and **logs** streams require the **containers** section. A signed-in
user without it can no longer stream a container's data.
