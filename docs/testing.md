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

- **CI runs tiers 1–2 only.** Everything touching a real daemon is developer-run,
  because GitHub's runners would need Docker-in-Docker and provisioned sidecars.
  A green CI badge therefore means "unit + adversarial tests pass", not "verified
  against real daemons".
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
