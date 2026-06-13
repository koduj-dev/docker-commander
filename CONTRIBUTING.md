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
internal/           # Go backend (api, auth, store, docker, monitor, ws, history, config, crypto)
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

```bash
go test -short ./...     # fast unit tests — this is what CI runs
go test ./...            # also runs integration tests (need Docker; some spin
                         # throwaway Redis / OpenLDAP / MailHog containers and
                         # skip cleanly when those aren't available)
cd web && npx tsc --noEmit   # type-check the frontend
```

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
  `*pentest*` tests for the style. Keep `go test -short ./...` green.

This is guidance, not tooling, but it's how the security-sensitive parts of the
codebase have been built.

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
