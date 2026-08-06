# Contributing to Docker Commander

Thanks for your interest in improving Docker Commander! This guide covers how to
build, test and submit changes.

By participating you agree to follow our [Code of Conduct](CODE_OF_CONDUCT.md).

## Ways to contribute

- **Report bugs** and **request features** via [issues](../../issues) (use the
  templates). For security problems, **do not** open a public issue — see
  [SECURITY.md](SECURITY.md).
- **Improve docs** under [`docs/`](docs/) or the README.
- **Send pull requests** — for anything non-trivial, please open an issue first
  so we can agree on the approach.

## Project layout

```
cmd/dockercmd/      # main: wiring, config, server bootstrap
internal/           # Go backend (api, auth, store, docker, monitor, mcp, ws, history,
                    #   templates, backup, selfupdate, tlscert, service, config, crypto)
web/                # React + TypeScript SPA (Vite, Tailwind); built into web/dist and embedded
docs/               # per-feature user manual
deploy/             # systemd unit + config example
```

The production artifact is a **single CGO-free binary** with the UI embedded
(`go:embed web/dist`).

## Development setup

You need **Go ≥ 1.25**, **Node.js ≥ 18** (to build the UI) and a running
**Docker daemon** (the app talks to it; some tests use it).

```bash
git clone https://github.com/koduj-dev/docker-commander.git
cd docker-commander
make build      # builds the UI, then the binary with the UI embedded
./dockercmd     # http://127.0.0.1:8470
```

For UI work, run the API and the Vite dev server side by side:

```bash
make dev                                # API on :8470 (dev mode, permissive CORS)
cd web && npm install && npm run dev    # UI on :5173, proxies /api → :8470
```

> **The committed `web/dist` matters.** It lets `go build ./...` work without
> Node. If you change anything under `web/src`, rebuild it with `make ui` and
> **commit the regenerated `web/dist`** as part of your PR.

## Tests

[docs/testing.md](docs/testing.md) describes every tier, what each one proves and
what isn't covered. The commands:

```bash
go test -short ./...     # fast unit + adversarial tests — this is what CI runs
go test ./...            # also runs integration tests (need Docker; some spin
                         # throwaway Redis / OpenLDAP / MailHog containers and
                         # skip cleanly when those aren't available)
npm run test --prefix web    # frontend unit tests
cd web && npx tsc --noEmit   # type-check the frontend

# Remote/multi-host paths, against real separate daemons over TCP and SSH:
scripts/remote-test-daemon.sh up 2
eval "$(scripts/remote-test-daemon.sh env)"
go test ./internal/docker/ -run 'RemoteBindDeploy|MultiHost' -count=1 -v
scripts/remote-test-daemon.sh down
```

> Pass **`-count=1`** for anything touching a real daemon: Go caches test results
> and an env-var change doesn't invalidate the cache, so a re-provisioned daemon
> will otherwise replay the previous verdict.

Before changing behaviour, skim [docs/gotchas.md](docs/gotchas.md) — it lists the
things in this codebase that have already caught someone out. Local setup and the
throwaway services the tests expect are in
[docs/dev-environment.md](docs/dev-environment.md).

**Adding a test? Prove it can fail.** Break the thing it tests, watch it fail,
check it failed for the *right* reason, then restore. Tests in this repo have
passed while guarding nothing — usually because something else (compose's own
validation, a shared status code) was doing the rejecting, and more than once
because the test asked a neighbouring question, or the environment answered it.
`docs/testing.md` lists the specific failure modes and the fixture traps that go
with real-daemon tests. Restore from a copy you took first, not with
`git checkout` — mutation testing means editing real files, and that command has
eaten uncommitted work here.

**Fixing a review finding? The fix needs reviewing too.** On the 1.6.0
authentication work, four consecutive rounds each found a new defect inside the
previous round's fix. Before calling one done, ask the inverse question — a check
needs a matching record, a record needs someone reading it, state cleared on
failure may need to be cleared only on success — and then ask what the fix makes
*worse*.

> ⚠️ **The integration tests run against your *real local* Docker daemon.** They
> create and clean up their own throwaway resources, but **never** add a
> host-global operation (`docker {system,network,image,volume} prune`) to a
> test — it would wipe the developer's own resources, not just the test's. If
> you only want the safe, deterministic run, use `go test -short ./...`.

Please add or update tests for behaviour you change. New backend code should
come with coverage; heavy integration tests are gated behind `testing.Short()`
so the default CI run stays deterministic.

## Review discipline

This app controls Docker daemons, so review changes accordingly — proportionally
to the risk of the change:

- **Before a commit** — read your own diff for correctness *and* security (auth /
  permissions, input handling, secret exposure, unsafe defaults) and fix what you
  find.
- **Before a PR** — do a full code + security review of the whole branch, and for
  any **new attack surface** (auth, parsers, endpoints, anything taking external
  input) **add adversarial tests** asserting the attack is rejected — see the
  `TestPen_*` cases in `*_pentest_test.go` (e.g. `internal/mcp/pentest_test.go`, `internal/api/oauth_pentest_test.go`). Keep `go test -short ./...` green.
- **When a bug turns out to be an instance of a class, sweep the class.** Patching
  the three tools that authorized against the wrong host is half the job; the half
  that lasts is a test enumerating every tool with that shape, which fails on a new
  one nobody has decided about (`internal/mcp/tool_host_scope_coverage_test.go`,
  `tool_authz_coverage_test.go`). Prefer a check derived from the real registry —
  routes, advertised tools — over a hand-maintained list, which goes stale silently.

This is guidance, not tooling, but it's how the security-sensitive parts of the
codebase have been built.

Beyond per-change review, the tree is periodically swept end-to-end by an
**adversarial review on Claude Fable 5** — independent reviewers per lane (auth &
crypto, authorization, untrusted input, backend correctness, frontend), each told
to refute a finding before reporting it. It is not a smoke test and it is not a
tier: it finds what to test, and a finding is only handled once it lands as a fix
plus a test that fails without it. See
[docs/testing.md](docs/testing.md#adversarial-review--and-why-it-is-deliberately-not-a-tier).

## Code style

- **Go must be `gofmt`-clean.** CI enforces it with
  `gofmt -l $(git ls-files '*.go')` — note that checks only **tracked** files, so
  run `gofmt -w` *after* `git add` (or format before staging). Also keep
  `go vet ./...` clean.
- **TypeScript must type-check** (`tsc --noEmit`); match the surrounding style.
- Write code that reads like the code around it — match naming, comment density
  and idioms. Keep comments about *why*, not *what*.

## A few project conventions

- **Database migrations are additive.** Add idempotent `ALTER TABLE … ADD
  COLUMN … DEFAULT …` statements (tolerating "duplicate column"); never write
  destructive migrations. SQLite via `modernc.org/sqlite` (no CGO).
- **Object refs** (image/container names) contain `:` and `/`, so pass them as
  **query params**, not chi path segments.
- Request bodies use `DisallowUnknownFields()` — strip read-only fields
  client-side.

## Pull requests

1. Branch off `main`.
2. Keep commits focused; write imperative, descriptive messages (what & why).
3. Make sure `go test -short ./...`, `go vet ./...`, the `gofmt` gate and the
   frontend type-check all pass; rebuild `web/dist` if you touched the UI.
4. Update [`docs/`](docs/) and add a [`CHANGELOG.md`](CHANGELOG.md) entry for
   user-facing changes.
5. Open the PR against `main` and fill in the template. CI must be green.

## License

By contributing, you agree that your contributions are licensed under the
project's [MIT License](LICENSE).
