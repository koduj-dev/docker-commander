# MCP — remote control from AI tools

[← Manual index](README.md)

Docker Commander can expose a **Model Context Protocol (MCP)** server so AI tools
— **Claude Code**, **Claude Desktop**, **Cursor**, and any MCP-capable client —
can monitor and **safely operate** your Docker hosts *as you*, with the **same
permissions** you have in the UI.

It is **off by default** and, when enabled, never exceeds your rights: every call
goes through the app's RBAC, tokens can only **narrow** your access, and the tool
set is a deliberate allow-list of **reads + safe control** — there is no `exec`,
image export, volume-content read, `prune` or `remove`.

## Enabling it

The server is gated by a config knob and should run **behind HTTPS** (native TLS
or a reverse proxy):

```ini
# /etc/docker-commander/commander.conf
DC_MCP_ENABLED=1
# Only needed for the OAuth flow (Claude Desktop / Cursor); bearer tokens work without it:
DC_MCP_PUBLIC_URL=https://docker.example.com
```

When disabled, the MCP and OAuth routes are **not mounted** — a request to `/mcp`
is just an unknown path (it falls through to the SPA, or a plain `404` when no UI
is embedded), with no hint the feature exists. The startup log says `MCP server:
disabled` so you can confirm the state at a glance.

## Two ways to authenticate

| Client | Auth | Notes |
|--------|------|-------|
| **Claude Code**, scripts, Cursor (header mode) | **Bearer API token** | Simplest. Create one on the **MCP Access** page (see below). |
| **Claude Desktop**, claude.ai, Cursor (connector) | **OAuth 2.1** | Needs `DC_MCP_PUBLIC_URL`. You log in to Docker Commander in the browser and approve a consent screen. |

### Bearer API tokens (the MCP Access page)

Open **MCP Access** in the sidebar. Each user manages **their own** tokens:

- Give it a **name**, an optional **expiry**, and optionally restrict it to a
  **subset of your sections** and/or mark it **read-only**.
- The secret is shown **once** (only a hash is stored) — copy it now. The page
  also gives you a ready-to-paste command:

  ```bash
  claude mcp add --transport http docker-commander \
    https://docker.example.com/mcp \
    --header "Authorization: Bearer <token>"
  ```

A token can only ever *narrow* your rights; if your account is read-only, every
token you mint is read-only too. Revoke a token anytime — it stops working
immediately.

### Admin overview (the MCP Admin page)

Administrators get a second page, **MCP Admin** (under *System*), with a
fleet-wide view: **every user's** active API tokens (each labelled with its
owner) and all registered **OAuth clients**. From here an admin can **revoke**
any token or **remove** any OAuth client — removing a client also purges the
authorization codes and refresh tokens issued to it, so anything connected
through it must re-authorize. Only metadata is shown; secrets are never
recoverable here. This makes a shared instance team-ready: you can audit and cut
off MCP access for the whole fleet from one place.

### OAuth (Claude Desktop / Cursor connector)

Add a **custom connector / remote MCP server** in your client pointing at
`https://<your-host>/mcp`. The client discovers the authorization server,
**registers itself** (dynamic client registration), and opens a browser to
Docker Commander. Sign in as usual, then **approve** the consent screen — you can
grant **full** or **read-only** access. Docker Commander never sees a password
here; it reuses your existing login session.

Under the hood this is a standard, self-contained **OAuth 2.1** server (PKCE,
exact redirect matching, audience-bound short-lived access tokens, rotating
refresh tokens). No external identity provider is required.

## What the AI can do

**Read** (`containers`, `images`, `projects`, `volumes`, `networks`, `logs`,
`events`, `dashboard`, `hosts`, `audit` sections — gated per token/user):

- list hosts, containers, images, volumes, networks, Compose projects
- inspect a container (config, mounts, health — **environment variables are
  omitted**), tail its **logs** (size-capped), read a project's **compose file**
- host **system info**, a resource **stats** snapshot and per-container **metrics
  history**, recent Docker **events**, and recent **audit** entries

**Safe control** (write — blocked for read-only tokens/users):

- **start / stop / restart** a container
- **deploy / down** a managed Compose project

It also exposes MCP **resources** (the container inventory and compose files as
attachable context) and **prompts** (curated workflows like *diagnose an
unhealthy container* or *guided safe redeploy*).

> Deliberately **not** available — by design, to avoid turning an AI token into a
> data-exfiltration or destruction path: `exec`/shell, image `save`/export,
> reading volume **contents** or arbitrary files, `kill`, `prune`, and `remove`.

## Security model

- **RBAC is reused, not reinvented.** Every tool maps to a section + read/write
  and is checked against your **live** permissions on **every** request — disable
  a section for a user and the matching MCP tool stops working immediately.
- **Tokens only narrow.** A token's section subset and read-only flag are applied
  *before* your own RBAC; they can never grant more than you have.
- **Secrets are kept out.** Container env vars, audit detail, and raw event
  attributes are omitted from tool output; logs are size-capped.
- **Off by default, behind HTTPS.** Enable it consciously. Access tokens are
  signed with a key dedicated to MCP, separate from your login session secret.
- Every **control** call (start/stop/restart, deploy/down) is written to the
  [audit log](audit.md) under your account.

## Tips
- Keep MCP **behind a reverse proxy / HTTPS**; the OAuth and rate-limited
  registration endpoints assume it.
- Hand an AI tool a **read-only** token (or one scoped to a few sections) when you
  only want it to *look*, e.g. to review how your stacks are wired.
- Lost a token? You can't recover the secret — **revoke** it and create a new one.
