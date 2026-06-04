# Docker Commander — roadmap & project notes

_Working/roadmap doc. The user-facing feature list lives in
[README.md](./README.md); this file is the **intent, what's shipped, what's
next**, plus dev/test notes._

## 🎯 What we want from it

A **single self-hosted binary** that lets a team **monitor and fully operate
Docker** across one or many hosts — from a clean enterprise UI — without
standing up a database or agents. Safe to expose on a server (auth, 2FA, RBAC,
encrypted secrets), useful locally out of the box, and friendly to ops
(Prometheus, webhooks, email, systemd).

## ✅ Shipped (v1)

- **Monitoring** — live CPU/mem graphs, historical charts (Redis/in-memory), aggregated logs (level detection, regex, structured parse rules), events feed, diff/top/df, raw inspect.
- **Control** — full container lifecycle (create/run, rename, update, commit, exec shell), in-container file browser (`docker cp`), images (pull/build/push/tag/save/load/import/history/prune), volumes & networks CRUD.
- **Multi-host** — local / TCP+TLS / SSH (verified host keys); the alert engine watches every host.
- **Alerting** — state / resource / log / restart rules (editable) → webhooks, email (SMTP, per-host recipient), in-app feed, Prometheus `/metrics`.
- **Security & admin** — Argon2id + TOTP 2FA (localhost-exempt option), multi-user with roles / per-section permissions / read-only, feature flags, audit log, optional **LDAP**; secrets encrypted at rest.
- **Ops** — single CGO-free binary, embedded UI, systemd unit + env example, cross-compiled releases.

## 🔭 Next / ideas (not yet built)

- **Built-in TLS cert helper** — a `dockercmd --make-certs` subcommand to obtain certs without external tooling: generate a **self-signed** cert for internal/LAN use, or drive an **ACME / Let's Encrypt** flow for public hosts. _Why: enable HTTPS with zero "ohýbátka" — no `openssl`, no reverse proxy. Native TLS via `DC_TLS_CERT`/`DC_TLS_KEY` already ships; this removes the cert-wrangling step._
- **CI/CD release pipeline** — tag → cross-compiled binaries on the Releases page (workflow in `.github/workflows/`). _Why: one-click installs per OS._
- **LDAP group → section mapping** — map directory groups to specific sections, not just admin. _Why: manage permissions in the directory, not per-user here._
- **OIDC / SSO** — Google/Azure/Okta login. _Why: enterprises standardise on SSO; LDAP is step one._
- **Alert-rule import/export** — JSON bundle of rules. _Why: reproducible setups across deployments._
- **Section-gated WebSocket** — the shared stats/logs WS (`/api/ws`) is not section-gated today; RBAC is enforced on REST. _Why: tighten read access for restricted users._
- **Container create from compose / templates** — beyond the single-container form. _Why: real stacks._

## ⚠️ Gotchas worth remembering

- `internal/api/respond.go` `decodeJSON` uses `DisallowUnknownFields()` — request bodies must contain **only** struct-declared fields (read-only fields like `hasPassword` must be stripped client-side; see `smtpPayload`/`ldapPayload`).
- Image/object refs contain `:` and `/`, so pass them as **query params**, not chi path segments (chi won't decode `%3A`).
- Alert-rule cooldown: `docker stop` emits several events (kill→die→stop); a 1s cooldown can double-fire — defaults are 60s.
- **Go `nil` slices marshal to JSON `null`, not `[]`** — the SPA then crashes on `x.length`/`.map`. Initialise API-returned slices (`make`/`[]T{}`) so empty means `[]`, and still guard with `?? []` on the TS side. (Bit us with `ResourceOverview.Containers` when no containers were running.)

## 🛠️ Dev / test notes (this machine)

- **Go** is at `~/.local/go` — not on default PATH: `export PATH="$HOME/.local/go/bin:$PATH"` (`GOTOOLCHAIN=local`).
- **Don't use `pkill`** in this sandbox (exits 144 and aborts the rest of the command); stop background servers explicitly (`lsof -ti tcp:PORT | xargs kill`).
- **Build:** `make build` (UI then binary). The committed `web/dist` lets `go build ./...` work without Node.
- **Headless UI verification:** puppeteer-core driving the system `google-chrome`, with a Node TOTP helper to pass mandatory 2FA (controlled inputs need the native value-setter + `input` event; checkboxes nested in `<label>` double-fire on click — use focus+Space).
- **Local test services:**
  - Redis: `docker run -d --rm -p 6399:6379 redis:7-alpine` → `DC_REDIS_ADDR=127.0.0.1:6399`.
  - Auth registry: `registry:2` with an htpasswd file (no `htpasswd` here → generate the bcrypt line via Go `x/crypto/bcrypt`). localhost is insecure-by-default for the daemon.
  - sshd: `osixia`-free alpine `apk add openssh` one-liner; host key is exchanged before auth (`DC_SSH_INTEGRATION` runs `TestSSHHostKeyEndToEnd`).
  - SMTP sink: `docker run -d -p 1025:1025 -p 8025:8025 axllent/mailpit` (HTTP API on :8025 to read mail).
  - LDAP: `osixia/openldap:1.5.0` (`LDAP_DOMAIN=example.org`, admin `cn=admin,dc=example,dc=org`); `ldapadd` a user.
