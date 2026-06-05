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
- **Compose** _(v1.2)_ — discover & manage **stacks** by label (incl. CLI-created): start/stop/restart/remove, view compose file. **Projects**: managed compose *folders* (compose + sidecar configs/scripts) edited in a built-in tree editor and deployed via the `docker compose` CLI (profiles, `.zip` import/export, redeploy/down); deployed projects surface as stacks.
- **Multi-host** — local / TCP+TLS / SSH (verified host keys); the alert engine watches every host; a host can be **disabled** to skip it (offline laptop, maintenance).
- **Alerting** — state / resource / log / restart rules (editable) → webhooks, email (SMTP, per-host recipient), in-app feed, Prometheus `/metrics`.
- **Security & admin** — Argon2id + TOTP 2FA (localhost-exempt option), multi-user with roles / per-section permissions / read-only, feature flags, audit log, optional **LDAP**; secrets encrypted at rest.
- **Ops** — single CGO-free binary, embedded UI, systemd unit + env example, cross-compiled releases.

## 🔭 Next / ideas (not yet built)

- **Compose — remaining** — the v1.2 Projects/Stacks features cover most of this (deploying via the `docker compose` CLI gives profiles, `build:`, `configs` and init containers for free). Still wanted: **API-based deploy** (`compose-go`) so plain-**TCP** hosts (no CLI, no host filesystem) can deploy too; **edit & redeploy for CLI-discovered stacks** (upload the YAML — `config_files` only points to a host path); **SSH / remote Projects** (currently local-host only); uploading `build:` contexts.
- **Upgrade the project file editor (syntax highlighting / validation)** — the Compose Projects feature already ships a working **multi-file tree editor**, but each file edits in a plain `<textarea>` (`web/src/pages/Projects.tsx`). Replace it with a real code editor — **CodeMirror 6** (lighter) or **Monaco** (heavier, lazy-load) — with YAML/Dockerfile/shell highlighting, `monaco-yaml` + the official **compose JSON Schema** for schema-aware autocomplete + inline validation, and an authoritative server check via `compose-go` (the same parser that deploys). _Why: editing compose/configs/scripts in a bare textarea is error-prone; highlighting + validation catches mistakes before deploy._
- **Networks & Topology at scale** — both views get cluttered/unreadable with many containers. _Why: real hosts run dozens of containers; the current graph and network list don't stay legible._ Ideas: search/filter, group or cluster by network/stack/label, collapse/expand groups, hide stopped, better auto-layout, virtualization/lazy rendering for large graphs, and a compact list fallback.
- **Built-in TLS cert helper** — a `dockercmd --make-certs` subcommand to obtain certs without external tooling: generate a **self-signed** cert for internal/LAN use, or drive an **ACME / Let's Encrypt** flow for public hosts. _Why: enable HTTPS with zero "ohýbátka" — no `openssl`, no reverse proxy. Native TLS via `DC_TLS_CERT`/`DC_TLS_KEY` already ships; this removes the cert-wrangling step._
- **CI/CD release pipeline** — tag → cross-compiled binaries on the Releases page (workflow in `.github/workflows/`). _Why: one-click installs per OS._
- **LDAP group → section mapping** — map directory groups to specific sections, not just admin. _Why: manage permissions in the directory, not per-user here._
- **OIDC / SSO** — Google/Azure/Okta login. _Why: enterprises standardise on SSO; LDAP is step one._
- **Alert-rule import/export** — JSON bundle of rules. _Why: reproducible setups across deployments._
- **Section-gated WebSocket** — the shared stats/logs WS (`/api/ws`) is not section-gated today; RBAC is enforced on REST. _Why: tighten read access for restricted users._
- **Per-host monitoring health** — track each host's reachability in the monitor (the events stream already backs off exponentially when a host is down) and surface it: a 🔴 "unreachable" indicator on the Hosts page (and per-host badge), plus an optional alert when a host goes offline / recovers. _Why: when a host drops (laptop offline, daemon down) you want to *see* it, not just find retries in the log._

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
