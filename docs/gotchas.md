# Gotchas worth remembering

[← Manual index](README.md)

Things that cost real debugging time here, kept so they cost it once. This is
engineering knowledge about *this* codebase — not a style guide, and not a list
of everything that could go wrong. If something bit you and the cause was
non-obvious, it belongs here.

Testing has its own set: see [Testing](testing.md), which covers mutation-testing
every guard and the fixture traps that come with a real Docker daemon.

## API and data shapes

- **`decodeJSON` uses `DisallowUnknownFields()`** (`internal/api/respond.go`), so a
  request body must contain **only** struct-declared fields. Read-only fields such
  as `hasPassword` have to be stripped client-side — see `smtpPayload` /
  `ldapPayload` for the pattern.
- **Image and object refs contain `:` and `/`**, so pass them as **query params**,
  never as chi path segments: chi will not decode `%3A`.
- **Go `nil` slices marshal to JSON `null`, not `[]`.** The SPA then crashes on
  `x.length` / `.map`. Initialise API-returned slices (`make` / `[]T{}`) so empty
  means `[]`, and still guard with `?? []` on the TypeScript side. This bit us via
  `ResourceOverview.Containers` when no containers were running.
- **`ORDER BY` cannot be a bound parameter.** Anything sortable from the UI must
  map its sort key through a fixed whitelist (see `AlertQuery.orderBy`), or you
  are building SQL out of a query string.
- **SQLite `LIKE` has no default escape character.** Escaping `%` and `_` only
  works together with an explicit `ESCAPE '\'` clause — without it the backslashes
  are matched literally and the filter silently searches for the wrong string.

## Docker behaviour

- **`docker stats` CPU is per-core: 100% is one core.** A container busy on four
  cores reads ~400%, so any fixed threshold or dashboard built on it is wrong on a
  multi-core host unless divided by the core count. The engine exposes the count
  as `CPUCores` for exactly this.
- **`docker compose up -d` builds only a *missing* image.** Without `--build`, the
  second deploy after editing a Dockerfile or its context silently keeps running
  the old image and reports `Container Running`.
- **A build context is uploaded by Docker itself**; a bind mount is not. That is
  why remote deploys need bind seeding but nothing for `build:`. Conflating the two
  put a phantom item on the roadmap for months.
- **Alert-rule cooldown:** `docker stop` emits several events (kill → die → stop),
  so a 1s cooldown can double-fire. Defaults are 60s.
- **On older engines `/containers/json` lags `inspect`.** After stopping a stack,
  Engine 24 reported a container as `running` for ~250 ms while `inspect` already
  said `exited`. The app polls, so it is only a problem for tests that assume
  instantaneous consistency.

## Deployment

- **systemd `ProtectHome=true` breaks `docker compose` plugin discovery.** The
  shipped unit runs as a dedicated user with `ProtectHome=true`, which makes that
  user's home inaccessible (`EACCES`, not `ENOENT`). The docker CLI bases
  plugin discovery on its config dir (`~/.docker`) and treats `EACCES` there as
  fatal, so `docker compose` reads as an *unknown command*: `ComposeAvailable()`
  returns false and Projects' Deploy/Down are disabled. It works in a shell and
  not under the service, which is what makes it confusing. Fixed by
  `Environment=DOCKER_CONFIG=/var/lib/dockercmd/.docker` in
  `deploy/dockercmd.service`; the `install-*` scripts cover each OS.

## Secrets

- **`net/http` transport errors embed the full request URL.** `*url.Error`'s
  message includes it, so logging or storing `err.Error()` from a failed webhook
  call writes any token in that URL wherever the error goes. Report the wrapped
  cause instead (`redactURL` in `internal/monitor/webhook.go`).
- **A backup archive is equivalent to the plaintext of every stored secret**, because
  the at-rest encryption key is a row in the same database. Hence 0600 and the
  optional passphrase.

## Destructive operations

- **NEVER call a host-global prune (`Prune{Networks,Images,Volumes}`) from an
  integration test.** The integration tests run against the developer's *real*
  local daemon, and a global prune removes EVERY unused network / image / volume —
  not just the test's. This is exactly how a network lifecycle test once wiped a
  developer's networks. Test create / connect / remove on test-owned resources
  only, and exercise prune by hand on a throwaway host.
- **A plain file write follows a symlink at the destination.** Anywhere the app
  writes next to a user-controlled path, write to a temp file and rename — rename
  replaces the link instead of writing through it.
