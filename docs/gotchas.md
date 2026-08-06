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
- **`time.Duration` is int64 nanoseconds, so `days * 24 * time.Hour` overflows**
  above ~106,751 days. Asking for a 200,000-day token expiry produced a date in
  1989 — a credential dead the moment it was issued — and larger values wrapped to
  arbitrary dates. Validate the *number of days* against a ceiling before it ever
  becomes a `Duration`.
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
- **Container network stats are cumulative counters that reset on recreate.**
  Store the raw counters and derive rates at read time; a delta computed across a
  reset is negative, and rendering it produces either a phantom spike or a
  negative rate. `applyNetRates` skips the delta when the new counter is lower
  than the previous one, so a reset reads as a gap at zero.
- **`/containers/{id}/stats` is keyed by *interface*, not by network.** The API's
  `endpoint_id` field exists but the daemon fills it in on **Windows only**, which
  is why `docker stats` itself shows a single aggregate `NET I/O` column. Sum
  across interfaces and say so; anything per-network needs MAC/namespace
  inspection via netlink (Linux-only, hostile to remote hosts).
- **On older engines `/containers/json` lags `inspect`.** After stopping a stack,
  Engine 24 reported a container as `running` for ~250 ms while `inspect` already
  said `exited`. The app polls, so it is only a problem for tests that assume
  instantaneous consistency.

## HTTP timeouts and streaming

- **A handler runs from the moment the HEADERS arrive, not the body.** With no
  `ReadTimeout` a client can send headers and dribble the body indefinitely,
  holding a handler open — and any check the handler makes *before* reading the
  body is made against a state the client can then outlive. That is why the
  authorisation for pairing a second factor lives in the `INSERT` rather than in a
  check above it.
- **Re-arming a read deadline after the body ends cancels the request.** When a
  handler finishes reading, `net/http` clears the deadline itself and starts a
  background read to notice the client leaving (`startBackgroundRead`); any failure
  of that read is taken as "client gone" and cancels the request context. With
  `Content-Length`, the final read returns bytes **and** `io.EOF` together, so a
  naive "extend on n > 0" re-arms it at exactly the wrong moment. Every long
  `docker build` died two minutes after its context finished uploading.
- **Clearing that deadline on *any* error is the opposite mistake.** If the handler
  returns without draining the body, `net/http` drains the remainder inline, inside
  `chunkWriter.writeHeader`, *before* the response headers go out — unbounded if the
  deadline is gone. A client that declares a body, sends two bytes and goes quiet
  without closing pins a goroutine and an fd for the life of the process, and never
  receives the timeout the handler wrote. Clear on `io.EOF` only.
- **`Connection: close` skips that drain**, so a reproduction that closes the
  connection will not show the bug. Test stalled-body behaviour over keep-alive.
- **Hijacked connections are off the clock**: `net/http` clears deadlines on
  hijack, so WebSocket streams are unaffected by `ReadTimeout` — but an idle
  keep-alive connection now closes after it, because `Server.idleTimeout()` falls
  back to `ReadTimeout` when `IdleTimeout` is unset.

## Authorization

- **A sequential id is not an access control.** Any endpoint or tool that takes an
  integer id (project, alert, container-metrics series) must resolve the **host
  that record belongs to** and authorize against it — checking the section alone
  leaves the id space walkable, since ids are consecutive. Three MCP tools shipped
  checking their section against **host 0** while acting on someone else's record.
  The tell is machine-detectable: an integer id argument with no `host_id`.
- **A missing record and an out-of-reach one must answer identically.** Otherwise
  the error itself confirms the record exists, and the endpoint becomes a way to
  map what runs on hosts you cannot see.
- **Over-tightening is a bug too, not a safe default.** Host reach is derived from
  grants across *all* sections, so also demanding the `hosts` section for a
  host-scoped alert route hid alerts from users who legitimately reached that host.
  Guards need a test for what they must still **allow**.

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
