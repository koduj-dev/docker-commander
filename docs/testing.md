# How Docker Commander is tested

You are being asked to point this at your Docker daemons — often production ones,
sometimes over the network. That deserves a straight answer about what "tested"
means here, so this page describes the actual test tiers, **what each one proves**,
and, just as importantly, **what isn't covered**. Nothing on this page is
aspirational: every tier below exists in the repository and can be run by you.

The short version: the fast tests are the floor, not the ceiling. Anything that
touches a real Docker daemon is also tested **against real daemons** — including a
second and third one reached over TCP and SSH — because a mock daemon cannot tell
you whether a remote deploy actually works.

## The tiers

| Tier | What it proves | Where | Runs in CI |
|---|---|---|---|
| **1 · Unit** | Logic, parsing, edge cases, error paths | ~293 Go tests across 17 packages + 13 frontend (Vitest) tests | ✅ every push & PR |
| **2 · Adversarial (pentests)** | Attacks are *rejected* — not just that the happy path works | 31 cases in 4 `*_pentest_test.go` files, plus `PENTEST:`-marked cases alongside the features they guard | ✅ (they're plain unit tests) |
| **3 · Runtime smoke** | The real transport/CLI behaves as assumed — no mocks in the loop | MCP over real HTTP with the official SDK client; the Compose override resolved by the real `docker compose` | ⛔ needs Docker |
| **4 · Integration** | Real Docker daemon, real Redis / OpenLDAP / SMTP | 10 test files behind `testing.Short()`; throwaway containers, skipped cleanly when unavailable | ⛔ needs Docker |
| **5 · Multi-daemon end-to-end** | Remote operations against daemons that genuinely cannot see this machine | docker-in-docker sidecars over **TCP and SSH**, 1–3 at a time | ⛔ needs Docker + provisioning |
| **6 · Version matrix** | The app works on the Docker Engine *you* run, not just the maintainer's | Tier 4 re-run against pinned `docker:NN-dind` for Engine 24–28 | 🌙 nightly + on demand ([compat.yml](../.github/workflows/compat.yml)) |

### Tier 2 — why adversarial tests get their own tier

A test asserting "login works" says nothing about whether login can be *bypassed*.
So for every security-relevant surface the repo also carries tests that mount the
attack and assert it fails. Concretely, they cover things like JWT `alg=none` and
wrong-key forgery, OAuth code/refresh **replay**, redirect smuggling, CSRF,
**IDOR** (reaching another user's objects), PKCE downgrade, privilege escalation
by a read-only or section-restricted user, path traversal, and symlink escapes.

They live next to what they guard — e.g. `internal/mcp/pentest_test.go`,
`internal/api/oauth_pentest_test.go`, and the traversal/symlink cases in
`internal/docker/compose_binds_test.go`. When one of these finds a real hole, the
rule is to fix the hole and keep the test as a regression guard.

### Every guard is mutation-tested

A green test is not evidence that it tests anything. The rule here is to **break
the guard on purpose, watch the test fail, read *why* it failed, then restore**.

That has caught genuinely vacuous tests more than once, and the failure modes are
worth naming because they all look fine in review:

- **Something else was doing the rejecting.** A test for the compose editor's
  `.yml`-suffix rule passed with that rule deleted — the payload happened to be
  invalid YAML, so `docker compose config` refused it first. The test named one
  guard and exercised another. The fix is to make the payload valid in every
  respect *except* the one under test.
- **The guard's value is the message, not the rejection.** The empty-compose check
  is functionally redundant (compose rejects empty files anyway), so "an error came
  back" passed without it. What it actually buys is telling the operator that
  clearing the editor is not how you remove a stack — so the test asserts the
  message, with a comment saying why it asserts on a string.
- **The assertion was too weak to fail.** Asserting "not 200" survived removing a
  permission check, because a denial and an unrelated failure shared a status code.
  Fixed by making the responses distinguishable and asserting the specific one.

If you cannot make a test fail by breaking the thing it names, it is not testing
that thing.

### Fixture hygiene for daemon-backed tests

Real-daemon tests leave real state, and every one of these has cost debugging time:

- **`t.Context()` is cancelled just *before* `t.Cleanup` runs.** A cleanup closure
  that captured it can never reach the daemon, so removal fails silently and
  fixture containers survive every run. Pass the background context that
  `newManager` returns into helpers that register cleanups.
- **A pentest whose guard fails has real side effects.** When the attack under
  test is `compose up -d`, letting it through starts containers the fixture's own
  cleanup never sees — and they poison the *next* run, since `ListStacks` groups by
  label and a survivor contributes its stale `config_files` path. Clean up what the
  *attack* produced, not only what the fixture created (`freeStack`).
- **A killed run poisons the next one.** `t.Cleanup` doesn't run on a `-timeout`
  panic or Ctrl-C, so fixed-name containers survive and later runs fail with "name
  is already in use" (`freeName` frees the name first).

Check it rather than assume it: run the package twice and confirm
`docker ps -aq --filter name=dctest | wc -l` is 0 before and after.

### Tier 5 — the part that's hard to fake

Some behaviour simply cannot be verified against the local daemon, because the
bug only exists when the daemon is somewhere else. The clearest example: a Compose
project that bind-mounts its own config files. Deploy it to a *remote* daemon and
that daemon cannot see those files — it silently creates each missing source as an
empty **directory**, so a single-file config mount makes the container fail to
start outright. No unit test will tell you that.

So the remote paths are exercised against real, separate daemons:

```bash
scripts/remote-test-daemon.sh up 2    # docker-in-docker sidecars, TCP + real sshd
eval "$(scripts/remote-test-daemon.sh env)"
go test ./internal/docker/ -run 'RemoteBindDeploy|MultiHost' -count=1 -v
scripts/remote-test-daemon.sh down
```

That covers the full round trip over **both** remote transports (TCP, and SSH with
real key auth and a pinned host key), plus multi-host behaviour: retargeting a
project from one host to another, concurrent deploys to two hosts over *different*
transports, and two projects on one host not clobbering each other's data.

The provisioning script never reads or writes your `~/.ssh` — it uses a throwaway
key, its own `known_hosts`, and a dedicated single-key `ssh-agent`.

### Tier 6 — why a version matrix, and what it already caught

"Works on my daemon" is not an answer to "which Docker versions does this
support?". Users run whatever their distro ships, and both halves of the Docker
surface move: the Engine API (through the Go SDK) and the `docker compose` CLI.

The matrix doesn't add new tests — it **re-runs tier 4** with the app pointed at a
pinned `docker:NN-dind`, once per Engine major. `DC_COMPAT_DOCKER` swaps the
fixture's host row for that daemon, and because host id 0 falls back to the first
configured host, every existing test follows along unchanged.

It earned its place on the first run. Against **Engine 24 (API 1.43)** one test
failed that passes on Engine 29: after stopping a stack, `/containers/json` still
reported a container as `running` for ~250 ms, while `inspect` already said
`exited`. Newer engines are consistent immediately. The app polls that endpoint,
so a user sees the correct state on the next refresh — the *test* was asserting
instantaneous consistency, which is stricter than the app needs, and it now polls
with a bounded wait. Without the matrix, that difference would have surfaced as a
confusing bug report from someone on an older distro.

**A slow first run against a fresh dind is not a hang.** The daemon starts with an
empty image cache, so `alpine` is pulled from Docker Hub inside it, and the
lifecycle test spends 2 × 10 s in `restart` + `stop` because busybox `sleep`
ignores SIGTERM and Docker waits out the full grace period before SIGKILL. Budget
a couple of minutes per major; a tight `-timeout` turns that into a panic that
reads exactly like an incompatibility. (It did once — an "Engine 25 hang" that was
nothing of the sort.)

**A killed run poisons the next one.** `t.Cleanup` doesn't run when the binary
dies on a `-timeout` panic or a Ctrl-C, so containers with fixed names survive and
every later run fails with *"name is already in use"* — against a throwaway dind
that also reads like a version-specific break. The fixture now force-removes its
own names before creating them (`freeName`), so a poisoned daemon self-heals.

**Both env vars are required**, and forgetting one fails confusingly:
`DC_COMPAT_DOCKER` points the app's Docker client at the pinned daemon, while
`DOCKER_HOST` points the `docker compose` subprocess at the same one. Set only the
first and the Compose tests deploy to your own daemon, then hang waiting for
containers that started somewhere else.

## Running the tests

```bash
go test -short ./...              # tiers 1–2. Deterministic, no Docker. This is what CI runs.
go test ./...                     # adds tiers 3–4 (needs a Docker daemon)
npm run test --prefix web         # frontend unit tests
cd web && npx tsc --noEmit        # frontend type-check
gofmt -l $(git ls-files '*.go')   # formatting gate (run after `git add`)
go vet ./...
```

Two tiers need explicit setup, and skip cleanly with an explanatory message
otherwise:

```bash
DC_SSH_INTEGRATION=user@host:port go test ./internal/docker/ -run SSHHostKey   # live sshd
scripts/remote-test-daemon.sh up 2                                             # tier 5, above
```

Tier 6 — the version matrix — against one Engine major:

```bash
docker run -d --name dc-compat --privileged -e DOCKER_TLS_CERTDIR="" \
  -p 127.0.0.1:12375:2375 docker:24-dind --host=tcp://0.0.0.0:2375 --tls=false
DC_COMPAT_DOCKER=tcp://127.0.0.1:12375 DOCKER_HOST=tcp://127.0.0.1:12375 \
  go test -count=1 -run 'TestIntegration|TestCompat' ./internal/docker/
docker rm -f dc-compat
```

`TestCompatNegotiatedVersions` logs a `COMPAT=` line with the engine, negotiated
API and Compose versions, and fails if the negotiation lands below the floor in
`minAPIVersion`. That constant, not a sentence in a doc, is what makes the README
table true.

> **Pass `-count=1` for tiers 3–5.** Go caches test results, and an environment
> variable change does **not** invalidate that cache — a re-provisioned daemon will
> otherwise replay the previous run's verdict. This is easy to lose an hour to.

> ⚠️ **Tiers 3–4 run against your real local Docker daemon.** They create and
> remove only their own throwaway resources. A test must **never** call a
> host-global prune (`docker {system,network,image,volume} prune`) — that would
> delete the developer's own unused resources, not just the test's. If you want
> only the safe, deterministic run, use `go test -short ./...`.

## What is *not* covered

Stated plainly, so nobody infers more than is there:

- **CI runs tiers 1–2 on every push**, and **tier 6 nightly**. Tiers 3–5 are
  developer-run, because GitHub's runners would need provisioned sidecars for the
  multi-daemon work. A green CI badge therefore means "unit + adversarial tests
  pass"; the nightly matrix badge is what says "and it still works on Engine
  24–28".
- **The matrix pins Engine majors, not every patch release.** `docker:24-dind`
  resolves to the newest 24.x at pull time, so a regression in a specific patch
  between nightly runs is not caught the moment it ships.
- **No automated browser/UI test suite.** The frontend has type-checking and unit
  tests for logic, and UI changes are verified by hand. (`scripts/screenshots/`
  drives a real browser, but it generates documentation images — it asserts
  nothing.)
- **No automated HTTP round trip for remote projects.** Tier 5 drives the Go
  packages directly; it does not go through a running `dockercmd` with login and
  2FA. The HTTP handlers themselves have unit tests.
- **Windows is cross-compiled and unit-tested, not integration-tested.** The
  `--install-service` path notably does not support Windows yet.
- **Coverage is informational.** CI prints statement coverage for the `-short`
  run: currently **~40% overall**, and higher where it matters most
  (`internal/crypto` ~91%, `internal/auth` ~59%). Read that number for what it is —
  it counts only tiers 1–2, so the integration and multi-daemon tiers, which carry
  much of the real assurance, contribute nothing to it. Conversely, a percentage
  says nothing about whether the *right* things are tested; the adversarial tier
  exists because coverage alone is a poor proxy for safety.

## If you're contributing

[CONTRIBUTING.md](../CONTRIBUTING.md) has the workflow. The expectations in short:
new behaviour comes with tests, and anything touching authentication,
authorization, crypto, or untrusted input also comes with adversarial tests that
assert the attack is rejected. Keep `go test -short ./...` green.
