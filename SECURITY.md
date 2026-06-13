# Security Policy

Docker Commander controls Docker daemons, so we take security seriously and
appreciate responsible disclosure.

## Supported versions

The project follows semantic versioning. Security fixes land on the **latest
released minor** and are published as a new patch/minor release.

| Version | Supported |
|---------|-----------|
| latest `1.x` | ✅ |
| older releases | ⚠️ best effort — please upgrade |

## Reporting a vulnerability

**Please do not open a public issue for security problems.**

Report privately through either:

- **GitHub** — *Security → Report a vulnerability* (private vulnerability
  reporting / advisories) on this repository, **or**
- **Email** — **filip@koduj.dev**.

Please include, as far as you can:

- a description of the issue and its impact,
- steps to reproduce or a proof of concept,
- affected version (see the UI footer or `GET /api/version`) and deployment
  details (binary / systemd, host kind, reverse proxy, OS).

We aim to acknowledge a report within a few days and to keep you updated on the
fix and disclosure timeline. We'll credit reporters who want to be named once a
fix is released. This is a community project maintained on a best-effort basis —
thanks for your patience.

## Scope & threat model

A few things worth knowing when assessing a report:

- **The Docker daemon socket is root-equivalent.** Docker Commander is intended
  to run behind authentication (Argon2id + optional TOTP 2FA), RBAC and — for
  anything public — TLS (native or a reverse proxy). It binds to **loopback by
  default**.
- Stored secrets (registry / SMTP / LDAP passwords) are **encrypted at rest**
  (AES-256-GCM) with a per-install key in the data directory.
- Reaching remote daemons over **plain TCP without TLS** is insecure by design;
  prefer SSH or TLS. See [docs/hosts.md](docs/hosts.md).
- **Self-update executes downloaded code, so it's verified.**
  `dockercmd --self-upgrade` only installs a GitHub release asset whose
  **SHA-256** matches the digest GitHub records (falling back to the release's
  `SHA256SUMS`), then replaces the binary atomically. The periodic update *check*
  (the admin banner) is an outbound call to the GitHub API and can be disabled
  with `DC_UPDATE_CHECK=0` on air-gapped hosts.
- **The MCP server (AI-tool access) is off by default.** Enable it with
  `DC_MCP_ENABLED` and run it behind HTTPS. When on, every request is
  authenticated (a hashed-at-rest **bearer API token** or an **OAuth 2.1** access
  token — PKCE, audience-bound, signed with a dedicated key) and authorized by
  the **same RBAC** as the UI, re-checked against the live user on every call.
  Tokens can only **narrow** their owner's rights (section subset + read-only),
  and the tool set is an allow-list of reads + *safe* control — no `exec`, image
  export, volume-content reads, `prune` or `remove`. See [docs/mcp.md](docs/mcp.md).

Generally **out of scope:** issues that require an already-compromised host or
data directory, exposing the app without the documented protections, or
vulnerabilities in Docker/third-party dependencies themselves (report those
upstream, though we're happy to bump versions).
