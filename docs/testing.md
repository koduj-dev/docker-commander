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
| **1 · Unit** | Logic, parsing, edge cases, error paths | ~533 Go tests across 17 packages + 73 frontend (Vitest) tests in 9 files, 3 of them DOM-backed | ✅ every push & PR |
| **2 · Adversarial (pentests)** | Attacks are *rejected* — not just that the happy path works | 83 cases in 13 `*_pentest_test.go` files, plus `PENTEST:`-marked cases in ~25 more files, alongside the features they guard | ✅ (they're plain unit tests) |
| **3 · Runtime smoke** | The real transport/CLI behaves as assumed — no mocks in the loop | MCP over real HTTP with the official SDK client; the Compose override resolved by the real `docker compose` | ⛔ needs Docker |
| **4 · Integration** | Real Docker daemon, real Redis / OpenLDAP / SMTP | 12 test files behind `testing.Short()`; throwaway containers, skipped cleanly when unavailable | ⛔ needs Docker |
| **5 · Multi-daemon end-to-end** | Remote operations against daemons that genuinely cannot see this machine | docker-in-docker sidecars over **TCP and SSH**, 1–3 at a time | ⛔ needs Docker + provisioning |
| **6 · Version matrix** | The app works on the Docker Engine *you* run, not just the maintainer's | Tier 4 re-run against pinned `docker:NN-dind` for Engine 24–29 | 🌙 nightly + on demand ([compat.yml](../.github/workflows/compat.yml)) |

### Tier 2 — why adversarial tests get their own tier

A test asserting "login works" says nothing about whether login can be *bypassed*.
So for every security-relevant surface the repo also carries tests that mount the
attack and assert it fails. Concretely, they cover things like JWT `alg=none` and
wrong-key forgery, OAuth code/refresh **replay**, redirect smuggling, CSRF,
**IDOR** (reaching another user's objects), **per-host scope bypass** (reaching a
host your grants don't cover, over REST *and* MCP), PKCE downgrade, privilege
escalation by a read-only or section-restricted user, path traversal, and symlink
escapes.

They live next to what they guard — e.g. `internal/mcp/pentest_test.go`,
`internal/api/oauth_pentest_test.go`, and the traversal/symlink cases in
`internal/docker/compose_binds_test.go`. When one of these finds a real hole, the
rule is to fix the hole and keep the test as a regression guard.

### Sweeps, not spot checks, for anything gated

A per-tool test only ever covers a tool somebody remembered to cover. What it
cannot catch is the **next** one — a tool added later that simply never calls the
access gate would run with no RBAC at all, and every existing test would still be
green. So the gated surfaces are swept by construction, from the list the server
actually advertises:

- **`TestEveryToolConsultsTheAccessGate`** calls every advertised MCP tool with a
  gate that denies everything and requires each to fail with that gate's own
  sentinel. A tool that skips the gate cannot produce it. `TestEveryToolRespectsTokenScope`
  does the same for a token's narrowing, and on the REST side
  `TestRBACEveryAPIRouteHasASectionDecision` requires every registered route to map
  to a section decision — so a new endpoint cannot arrive ungated by omission.
- **`TestNoDestructiveToolsAreAdvertised`** fails on any destructive verb —
  `exec`, `prune`, `remove`, image export — appearing in the tool list, so the
  read + safe-control boundary cannot erode one tool at a time.
- **`TestEveryRecordScopedToolChecksItsRecordsHost`** exists because the deny-all
  sweep passed while three tools were still broken. `preview_deploy`, `alert_delivery` and `acknowledge_alert`
  each authorized the right *section* against **host 0** while acting on a record
  belonging to another host. The distinguishing property is machine-detectable — a
  tool takes an integer id and **no** `host_id`, so the id implies a host nothing
  in the arguments names — so the sweep finds such tools itself and **fails on any
  it has no fixture for**. A new record-scoped tool cannot be added without
  somebody deciding how it is host-scoped.

The lesson generalises: when a bug turns out to be an instance of a *class*,
fixing the instance is half the work. The other half is a test that enumerates the
class.

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
- **The fixture made the assertion true by itself.** A logout test checked that the
  previous user's alerts are not replayed, using the *same* event ids before and
  after — so the "nothing new" result held whether or not the state was cleared.
  It passed with the teardown deleted. Fixed by making the post-logout poll return
  a newer id than the pre-logout baseline, so only a cleared baseline can produce
  an empty result.
- **Only refusals were asserted, so over-tightening looked like a pass.** Scoping
  the alert routes per host, it was tempting to also demand the `hosts` section —
  which reads as caution and is a different bug: host reach is derived from grants
  across *all* sections, so it hid alerts from users who legitimately reached that
  host. A guard needs a test for what it must still **allow**, not only for what it
  must deny.

Five more, from the passkey and read-timeout work — all of them tests written by
someone who had already read the list above, which is the point:

- **The assertion asked a neighbouring question.** A relying-party normalisation
  test checked whether passkeys were *available* on that host. A different code
  path decides that, so removing the normalisation changed nothing the test looked
  at. Assert on the thing under test — here, the relying-party id the ceremony
  actually hands the browser.
- **A coarser limit fired first.** A rolling-deadline test ran against the
  production two-minute idle window, so one generous deadline satisfied it and the
  *rolling* part was never exercised. The interval is a `var` so a test can shorten
  it below the total; that is what makes "extends on data" distinguishable from
  "set once".
- **The test framing skipped the code under test.** A stalled-upload reproduction
  sent `Connection: close`, and net/http skips its inline body drain when the
  response will close the connection — so the bug only appeared with keep-alive.
  The first attempt at reproducing a real finding "disproved" it.
- **The environment made it pass.** A frontend test stubbed
  `navigator.credentials` without an `@vitest-environment` docblock: green on a
  local Node 24, red on CI's Node 20, which has no global `navigator`. Pin the
  environment *and* assert it (`typeof window`), so the next person does not debug
  the symptom.
- **The test built its own instance, so it pinned the type and not the wiring.**
  Constructing `newCeremonies(limit)` or `newHTTPServer(...)` inside the test proves
  the constructor behaves; it says nothing about what production passes. Where the
  wiring is the guard — `main` must not hand-roll an `http.Server` without the read
  timeouts — the test reads the package's own source and refuses a second literal.
  That test then needed its own guard: it passed when the parse found no files, so
  it now counts the literal it expects and fails on zero.

If you cannot make a test fail by breaking the thing it names, it is not testing
that thing. And if the test is new, break it *before* believing it — the list above
is what happens when that step is skipped by someone in a hurry.

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

## Adversarial review — and why it is deliberately not a tier

Tests answer *"does the thing I thought of still work?"*. They cannot answer
*"what didn't I think of?"* — and on a tool that controls production Docker
daemons, that second question is the expensive one.

So the codebase is periodically swept by an **adversarial review**: several
independent reviewers, each confined to one lane so nobody's attention is spread
across everything —

| Lane | Ground covered |
|---|---|
| Authentication & crypto | password hashing, sessions, TOTP, LDAP, secrets at rest, backups |
| Authorization | the RBAC gate, named roles, per-host scoping, the MCP tool surface, OAuth |
| Untrusted input | path handling, archives, command execution, SSRF, SQL, resource caps |
| Backend correctness | concurrency, leaks, transactions, the alert engine, Docker integration |
| Frontend | injection surfaces, subscription hygiene, error handling, destructive-action guards |

They run on **Claude Fable 5**, and the prompts matter as much as the model: each
reviewer is told to read the code rather than infer it, to **try to refute every
candidate finding before reporting it**, to say whether a test already covers the
guard, and that an empty report is a respectable outcome. Without that last
instruction a reviewer invents work to look useful, which is worse than silence
because it buries the real findings.

**Why this is not tier 7.** A review is not reproducible, does not run in CI, and
guards nothing on its own — the next regression walks straight past it. It is a way
of *finding out what to test*, not a substitute for testing. So the rule is:

> A finding counts as handled when it lands as a **fix plus a test that fails
> without the fix** — and, where the finding is an instance of a class, plus the
> sweep that enumerates the class (see *Sweeps, not spot checks* above).

**Review the fix, not just the finding.** This is the expensive lesson from the
1.6.0 authentication work: across two pull requests, **four consecutive rounds each
found a new defect inside the previous round's fix**. Closing a slow-body hole
cancelled every long `docker build`; fixing *that* left a stalled upload pinning a
connection forever; a rate limit that only read its budget was replaced by one that
only wrote it; and moving a charge later to spare honest users made the endpoint
free precisely while under attack. None was caught by the person writing the fix.

A fix is the least-reviewed code in a pull request — everyone has already read the
surrounding lines, and it is written under the pressure of a known defect. So it
gets its own round, and before that a hand check of the **inverse question**, which
each of those four would have answered:

- added a check — is there a matching record? (a limiter's `Allow` needs a `Fail`)
- added a record — does anything read it? (a `Fail` needs an `Allow`)
- cleared state on failure — should it be only the *clean* case?
- deferred work to be kind to honest users — is it now free under attack?

And the question that outranks all of them: **what does this fix make worse?**
Trading a memory exhaustion for a lockout is not a fix.

**Treat every finding as a claim, not a verdict.** Machine reviewers produce
confident, well-written findings that describe code which does not exist; the
automated review on a recent PR filed two comments about imports the file never
had and about a missing `Bearer` prefix that was already there. Everything is
re-verified by reading the named lines before it is believed, let alone fixed. The
same discipline applies in the other direction — a finding that survives
verification is not softened because it is inconvenient.

**A second, per-PR pass with a different model.** Separate from the periodic
Claude Fable 5 sweep above, individual PRs/diffs also get a quick review from
**ChatGPT Codex** (currently `gpt-5.6-terra medium`) before merge — a different
model on the same diff surfaces different blind spots than either the author or
Claude alone would catch. Its output is written to a local, gitignored
`codex-review.md` at the repo root (refreshed per review, never committed — a
baseline for the next look, not a record of past ones) and is subject to the
exact same discipline as any other machine reviewer: every finding gets
verified against the named lines before being believed, per *Treat every
finding as a claim* above.

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
  24–29".
- **The matrix pins Engine majors, not every patch release.** `docker:24-dind`
  resolves to the newest 24.x at pull time, so a regression in a specific patch
  between nightly runs is not caught the moment it ships.
- **No automated browser/UI test suite.** The frontend has type-checking, unit
  tests for logic, and a small number of component tests that render against
  **happy-dom** — enough to pin wiring that unit tests cannot see, like "does the
  header actually call the hook that names the tab" and "does logging out really
  clear the previous user's alerts". It is not end-to-end coverage: layout,
  styling and real-browser behaviour are still verified by hand.
  (`scripts/screenshots/` drives a real browser, but it generates documentation
  images — it asserts nothing.)

  Those tests opt in per file with a `/** @vitest-environment happy-dom */`
  docblock, so the pure ones keep running in the faster node environment.
- **No automated HTTP round trip for remote projects.** Tier 5 drives the Go
  packages directly; it does not go through a running `dockercmd` with login and
  2FA. The HTTP handlers themselves have unit tests.
- **Windows is cross-compiled and unit-tested, not integration-tested.** The
  `--install-service` path notably does not support Windows yet.
- **Coverage is informational.** CI prints statement coverage for the `-short`
  run: currently **~45% overall**, and higher where it matters most
  (`internal/crypto` ~91%, `internal/store` ~70%, `internal/auth` ~67%). Read that
  number for what it is —
  it counts only tiers 1–2, so the integration and multi-daemon tiers, which carry
  much of the real assurance, contribute nothing to it. Conversely, a percentage
  says nothing about whether the *right* things are tested; the adversarial tier
  exists because coverage alone is a poor proxy for safety.

## If you're contributing

[CONTRIBUTING.md](../CONTRIBUTING.md) has the workflow. The expectations in short:
new behaviour comes with tests, and anything touching authentication,
authorization, crypto, or untrusted input also comes with adversarial tests that
assert the attack is rejected. Keep `go test -short ./...` green.
