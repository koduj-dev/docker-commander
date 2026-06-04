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

- **Docker Compose / stacks** — deploy and manage multi-container stacks, not just single containers. _Why: real apps ship as a `docker-compose.yml`; people want to bring a stack up/down as a unit._
  - **Approach:** parse the compose file with [`compose-spec/compose-go`](https://github.com/compose-spec/compose-go) and create networks / volumes / containers through the **existing Docker API** (not by shelling out to the `docker compose` CLI). Keeps the **single CGO-free binary** with no runtime deps, and works on **remote hosts** (local/TCP/SSH) since everything goes over the API.
  - **Discover existing stacks (CLI-created too):** group containers/networks/volumes by the standard `com.docker.compose.project` / `.service` labels. So stacks started with `docker compose up` on the host show up and get **lifecycle ops (stop/start/restart/remove) by label** for free. We label our own deployments compatibly so the host CLI still sees them.
  - **Edit / redeploy** only for stacks we have the YAML for (we persist it). For CLI-created stacks the original `compose.yml` isn't reachable over the Docker API (`com.docker.compose.config_files` points to a host path; readable over SSH only) — offer lifecycle + view, and "edit & redeploy" once the user uploads the file.
  - **Scope:** v1 = images, container_name, command, environment/env_file, ports, named + bind volumes, networks, restart, labels, `depends_on` (start order). **Wanted next:** `profiles`, `build:` contexts (upload + API build), healthcheck-conditioned `depends_on`, `configs`/`secrets`, `extends`, multiple compose files / overrides. Filip specifically wants **profiles** and broader compose-feature coverage.
  - **Caveat:** managing one stack from both DC and the host CLI can drift (compose `config-hash` won't match → `compose up` may recreate). Guidance: manage a given stack from one place.
- **In-app editor for Dockerfiles & compose files** — _pairs with the Compose epic above; planned for the same cycle (the editor needs a target: edit → deploy/build)._ A real code editor in the UI with highlighting, validation and autocomplete.
  - **Editor:** Monaco (best IntelliSense, but heavy — lazy-load / code-split) or CodeMirror 6 (lighter).
  - **compose:** `monaco-yaml` + the official **compose JSON Schema** → schema-aware autocomplete + inline validation client-side, plus an authoritative server check via the `compose-go` loader (same parser that deploys).
  - **Dockerfile:** highlighting + static instruction/flag completion; server-side syntax validation via buildkit's `dockerfile/parser` (Go, fits the single binary). Hadolint-style lint later.
  - **Storage:** a **Templates/Files library** in the DB — named compose/Dockerfile files, editable, deployable (stack) / buildable (image) straight from the editor.
  - **Endpoints:** `POST /api/validate/compose`, `POST /api/validate/dockerfile` (reused at deploy/build time).
  - **Caveats:** bundle size (lazy-load the editor); build/edit is arbitrary-build territory but already covered by image-build RBAC + audit.
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
