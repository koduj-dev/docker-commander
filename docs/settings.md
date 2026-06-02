# Settings

[← Manual index](README.md)

_Admin only._ App-wide configuration.

## Feature flags (enabled features)
Turn whole menu sections **on/off for everyone**. A disabled section is hidden
from the menu and its API is blocked — useful to trim the app to what your team
actually uses. Admins re-enable them here.

## Localhost 2FA exemption
By default **2FA is mandatory** for all logins. Enable this to let connections
from **loopback** (`127.0.0.1` / `::1`) log in with a password only (skipping
both the enrollment gate and the TOTP challenge). Remote connections always
require 2FA.

- It trusts the connection's `RemoteAddr` only (not forwarded headers), so
  **keep it off behind a reverse proxy** — otherwise every proxied request looks
  like localhost.
- Good for a personal/local install; leave off for shared servers.

## LDAP / Active Directory
Optional external authentication.

- **Enable** + **Server URL** (`ldap://host:389` or `ldaps://host:636`),
  optional **StartTLS**.
- **Bind DN** + **password** — a service account used to search (encrypted at
  rest); leave password blank to keep the stored one.
- **User base DN** and **User filter** (must contain `%s`, e.g. `(uid=%s)` or
  `(sAMAccountName=%s)`).
- **Admin group DN** (optional) — members are provisioned as admins.
- **Test** verifies dial / bind / search.

**How login works:** local accounts always use their local password. A username
with no local account (while LDAP is enabled) is authenticated against the
directory and **provisioned as a local `user`** (or `admin` if in the admin
group) so you can then grant sections in [Users](users.md). Such users can still
enroll their own TOTP.
