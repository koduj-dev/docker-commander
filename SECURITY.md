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

## What review this code has had — and what it hasn't

Every change is reviewed for correctness and security before it merges, and
anything touching authentication, authorization, crypto or untrusted input ships
with adversarial tests that assert the attack is *rejected*. On top of that, the
tree is periodically swept end to end by an **adversarial review on Claude
Fable 5** — independent reviewers per lane (auth & crypto, authorization,
untrusted input, backend correctness, frontend), each instructed to refute a
finding before reporting it. A finding is handled the same way a report from you
would be: a fix, plus a regression test that fails without it. The method and its
limits are written up in [docs/testing.md](docs/testing.md#adversarial-review--and-why-it-is-deliberately-not-a-tier).

Stated plainly, so nobody infers more than is there:

- **There has been no third-party security audit.** The above is the maintainer's
  own reading and tooling, not an independent assessment. Treat it as diligence,
  not as assurance.
- **A review only covers what it was pointed at.** That some lane came back clean
  is evidence about that lane on that day, and nothing about the rest.
- **Findings are fixed before they are described publicly.** If you are running an
  older release, assume it has issues a newer one does not — upgrading is part of
  running this safely.

## Scope & threat model

A few things worth knowing when assessing a report:

- **The Docker daemon socket is root-equivalent.** Docker Commander is intended
  to run behind authentication (Argon2id + a second factor: TOTP or a passkey),
  RBAC and — for anything public — TLS (native or a reverse proxy). It binds to
  **loopback by default**.
- **Signing in with a passkey alone rests on user verification, and is off by
  default.** A passkey used as the whole login is treated as two factors —
  possession of the authenticator plus the PIN, fingerprint or face that unlocks
  it — so the assertion must carry the user-verification flag. That is demanded of
  the ceremony *and* re-checked on what comes back, because an authenticator may
  answer without it.

  It is **opt-in per account** and enabling it requires the account's password.
  That is deliberate: for a passkey that **syncs** between devices (iCloud
  Keychain, Google Password Manager) the verification can be satisfied on any
  device the credential reaches, so the account then rests on that platform account
  too. A passkey as a *second* factor keeps the password in front of it; as the
  whole login it does not. Accounts backed by a directory (LDAP) cannot use it at
  all — the directory is their authority.

  The password plus a second factor always remains a valid way in. This is the
  recovery story: no admin can reset another account's second factor, so a passkey
  that could be the *only* way in would make a lost device a lost account.
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
  Tokens can only **narrow** their owner's rights (a subset of their sections and
  of the **hosts** they reach, plus read-only), they **expire** by default
  (30 days, with an admin-set ceiling; never-expiring tokens are off unless
  enabled), and **changes are rate limited per user** so a runaway or stolen token
  is bounded. The tool set is an allow-list of reads + *safe* control — no `exec`,
  image export, volume-content reads, `prune` or `remove`. See
  [docs/mcp.md](docs/mcp.md).

## Invariants worth knowing before changing auth code

Three rules the authentication code depends on. Each exists because breaking it
produced a real, exploitable defect that a green test suite did not notice.

- **Authorisation is decided by the write, not by a check before it.** Adding a
  second factor to an account that already has one needs the password; adding the
  first does not. Those are decided minutes apart, and the gap belongs to the
  client — the WebAuthn library reads the request body, and a request whose body
  arrives slowly holds a handler open across it. So the condition lives in the
  `INSERT` (`store.CreateFactor`), the way `PairPendingFactor` claims a pending
  enrolment with a compare-and-swap.
- **What a ceremony demands must be re-checked on what comes back.** Asking the
  browser for user verification is a preference an authenticator may ignore. Both
  layers are kept, and the second is deliberately testable on its own — with the
  first in place the library refuses first, so the second would otherwise rot
  untested.
- **Ceremonies reachable without credentials get their own bounded store.** They
  used to share one with registration and the 2FA step, so flooding the public
  endpoint — which needs no account — meant nobody could pair a passkey and
  accounts whose only second factor is a passkey could not finish signing in.
  Trading memory exhaustion for a lockout is not a fix.

Generally **out of scope:** issues that require an already-compromised host or
data directory, exposing the app without the documented protections, or
vulnerabilities in Docker/third-party dependencies themselves (report those
upstream, though we're happy to bump versions).
