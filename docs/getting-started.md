# Getting started

[← Manual index](README.md)

## First run

1. Start the binary (`./dockercmd`) and open <http://127.0.0.1:8470>.
2. **Create the admin account** — the first account is always an `admin`. On
   the same screen you choose whether to **enable 2FA now** or **skip it for
   now** (leaving localhost password-only — handy for a local/dev box).
3. **If you enable 2FA** — scan the QR code with an authenticator app (Google
   Authenticator, Aegis, 1Password…) and enter the 6-digit code to confirm. You
   can change the localhost exemption later (see [Settings](settings.md)).

After that you log in with username + password + the current TOTP code.

## The layout

- A left **sidebar** groups the agendas (Compute, Network, Observability,
  System). What you see depends on your role and permissions.
- A **host switcher** appears at the top of the sidebar once more than one host
  is configured — it rebinds every view to the selected Docker host.
- The **account menu** (bottom-left) shows who you are and signs you out.

## Day-to-day basics

- The **[Dashboard](dashboard.md)** is the home view: host facts, disk usage,
  and the running containers.
- **[Containers](containers.md)** is where you operate workloads — start/stop,
  open a shell, browse files, read logs.
- Set up **[Alerts](alerts.md)** so problems reach you by webhook or email even
  when no one is watching the UI (the server monitors 24/7).

## Security model in one minute

- Passwords are hashed with Argon2id; sessions are `HttpOnly` cookies.
- **2FA (TOTP)** is enforced for everyone unless an admin enables the localhost
  exemption.
- **Roles**: `admin` (full access + administration) or `user` (limited to
  granted sections, optionally read-only).
- Stored secrets (registry / SMTP / LDAP passwords) are encrypted at rest.

See [Users & roles](users.md) and [Settings](settings.md) for administration.
