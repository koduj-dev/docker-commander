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

Manage them on **Users & roles → Roles**. Each card shows the role's grants, how
many accounts hold it, and whether it's built-in or yours. The editor sets every
section to **—** (not granted), **read** or **write**, with all thirteen visible at
once. Built-in roles open read-only — use **Duplicate** for an editable copy.

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
> `admin` if in the configured admin group). Grant them access by hand, or let
> the directory decide with **group mappings** — see below.

## Roles from LDAP groups
A group mapping in [Settings → LDAP](settings.md) grants **roles** (and, for older
configs, raw sections) to members of an LDAP group, matched on the group's full DN.
A user's access is the union over every mapped group they belong to, re-derived on
**each login**, so moving someone between groups in the directory takes effect the
next time they sign in — including having a role taken away.

Two things it deliberately cannot do:

- **It cannot make anyone an admin.** Only the configured *admin group DN* does
  that. A role cannot contain role management either, so no mapping — however
  generous — hands out the keys.
- **It cannot lock anyone out by referencing a deleted role.** A stale role id in
  a mapping simply grants nothing.

Whether the directory is authoritative for roles depends on whether you use them:

| Your mappings | What a login does |
|---|---|
| No mapping grants a role | Roles assigned by hand on the account are left alone |
| Any mapping grants a role | Roles are replaced by what the groups grant — hand-assigned ones are dropped |

That's so upgrading doesn't quietly strip roles from installs whose mappings were
written before roles existed. Once you map a role anywhere, assign roles in the
directory rather than per account. **Sections** work the other way round and always
have: as soon as *any* mapping exists, group membership is authoritative for a
non-admin's sections and manual edits are overwritten on the next login.

## Note on the live stream
RBAC is enforced on the REST API **and** on the shared live stats/logs
WebSocket (`/api/ws`): each subscription is authorised per channel, and both the
**stats** and **logs** streams require the **containers** section. A signed-in
user without it can no longer stream a container's data.

## Your own profile
Every signed-in user has a **profile page** (the person icon beside *Sign out*),
regardless of permissions:

- **Account** — username, account type, whether you sign in locally or through
  LDAP, when the account was created and last used, and your **alert e-mail**.
- **Security** — 2FA status, and **Pair a new authenticator** for a new phone.
  Starting that flow is safe: the authenticator you already have keeps working
  until you enter a code from the new one, so cancelling changes nothing.
- **Access** — the roles you hold, plus every section you can reach, whether you
  can change it, and which role granted it. Handy for answering "why can I see
  this?" without an admin.

It reads only your own account.
