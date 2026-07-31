---
name: feature-tests
description: Use whenever you add or change functionality in this repo (a handler, endpoint, store method, tool, parser, UI behaviour, anything). Two rules — write tests for the new behaviour, and when the change touches security (auth, tokens, crypto, permissions, input parsing, file/network/DB, or anything consuming external input) ALSO build adversarial pentests that assert attacks are rejected. Mirrors the repo's existing unit / runtime-smoke / *pentest* test styles. Load this before marking a feature done.
---

# Tests for new functionality — and pentests for anything security-related

When you add or change behaviour in this repo, two things are non-negotiable.
Scale the depth to the change, but never ship new behaviour untested.

## 1. Every new functionality gets tests

Write tests that actually exercise the new behaviour — not just that it compiles:

- **Happy path** — the feature does what it should for valid input.
- **Edge cases** — empty/zero/nil, boundaries, large inputs, missing optional
  fields, "not found".
- **Error paths** — bad input is rejected with the right error; failures don't
  panic, leak, or corrupt state.
- Prefer **table-driven** Go tests; keep them deterministic and fast so they run
  under `go test -short ./...` (heavy ones needing Docker go behind
  `testing.Short()`). For end-to-end behaviour, a focused integration/smoke test
  that drives the real surface beats mocking everything.

## 2. Security-relevant change → ALSO build pentests

First decide if the change is security-relevant. It is if it touches any of:

- **auth / authz** — login, sessions, tokens, permissions/RBAC, OAuth, consent
- **crypto** — signing, hashing, key handling, randomness
- **untrusted input** — request parsing, query/path params, headers, file uploads,
  archive extraction, anything from the network or another host
- **file / FS / network / DB** boundaries, command execution, redirects
- **secrets** — anything that could read, store, or expose credentials/PII

If yes, writing "it works" tests is not enough — **add adversarial pentests** that
*mount the attack* and assert it is **rejected**. Cover the classes that apply:

- auth **bypass / forgery** (e.g. `alg=none`, wrong key, tampered/forged token),
  **replay** (codes, refresh tokens, nonces), **audience/scope confusion**
- **injection** (SQL, command, path traversal, header/CRLF), **IDOR**
  (cross-user/-tenant access), **CSRF**, **open redirect**
- **downgrade** (e.g. PKCE plain vs S256), missing-auth, malformed/oversized input
- privilege **escalation** — can a restricted actor exceed its rights?

For each: craft the malicious request/input and assert a rejection
(`IsError` / 4xx / error returned / no state change). If an attack is *not*
rejected, that's a real finding — **fix it before shipping**, then keep the test
as a regression guard.

## 3. Prove the test can fail — mutation-test every guard

A green test is not evidence. **Delete or disable the guard, re-run, and confirm
the test fails — and read the failure message to confirm it fails for the RIGHT
reason.** Then restore. Do this for every security guard and every non-obvious
behaviour, before calling it done.

This is not paranoia; it has caught vacuous tests repeatedly in this repo:

- **A guard that something else also enforces.** `TestPentest…NonYAMLPathIsRefused`
  passed with the `.yml`-suffix rule deleted, because the *payload* was invalid
  compose and `docker compose config` rejected it first. The test named one guard
  and exercised another. **Fix:** make the payload valid in every respect except
  the one under test, so the guard is the only thing that can refuse it.
- **A guard whose value is the message, not the rejection.** The empty-compose
  check is functionally redundant (compose rejects empty files anyway), so
  asserting "an error came back" passed with the guard gone. What the guard
  actually buys is telling the operator that clearing the editor is not how you
  remove a stack. **Fix:** assert the message, and say in a comment *why* the
  assertion is on a string.
- **An assertion too weak to fail.** Asserting "not 200" passed even with a
  permission check removed, because a permission denial and an unrelated failure
  shared a status code. **Fix:** assert the specific status, and split the
  responses so they're distinguishable.

Corollary: if you cannot make a test fail by breaking the thing it tests, it is
not testing that thing.

## 4. Fixture hygiene — Docker-backed tests

Real-daemon tests leave real state. Three traps, all of which have cost real
debugging time here:

- **`t.Context()` is cancelled just BEFORE `t.Cleanup` runs.** A cleanup closure
  that captured it can never reach the daemon, so removal silently fails and
  fixture containers survive every run. Pass the background context (the one
  `newManager` returns) into helpers that register cleanups — never `t.Context()`.
- **A pentest whose guard fails produces real side effects.** If the attack is
  `compose up -d`, letting it through starts containers the fixture's own cleanup
  never sees. They then poison the *next* run — `ListStacks` groups by label, so a
  survivor contributes its stale `config_files` path and the test fails on a
  missing file instead of on what it is about. Clean up **what the attack
  produced**, not just what the fixture created (`freeStack` in
  `stacks_edit_pentest_test.go`).
- **A killed run poisons the next one.** `t.Cleanup` does not run on a
  `-timeout` panic or Ctrl-C, so containers with fixed names survive and every
  later run fails with *"name is already in use"* — which, against a throwaway
  daemon, reads exactly like a version-specific incompatibility. Fixtures using
  fixed names must free their own name first (`freeName` in `integration_test.go`).

**Verify it:** run the package twice in a row and check nothing is left
(`docker ps -aq --filter name=dctest | wc -l` → 0 before and after). A suite that
only passes on a clean machine is not passing.

Related: **`go test` caches results and an env-var change does NOT invalidate the
cache** — always `-count=1` for anything driven by `DC_*` env vars, or you will
read the previous run's verdict about a daemon you just replaced.

## Where the patterns live (copy the style)

- **Unit / table-driven** — throughout `internal/*/_test.go`.
- **Runtime smoke** (drive the real transport/HTTP end-to-end) —
  `internal/mcp/smoke_test.go`.
- **Pentests** (adversarial, assert-rejection) — `internal/mcp/pentest_test.go`
  (JWT/token attacks) and `internal/api/oauth_pentest_test.go` (OAuth-flow
  attacks: replay, redirect smuggling, CSRF, IDOR, PKCE…). Mirror these for any
  new auth/security surface.

## Before you call a feature done

- New behaviour has tests; security surface has pentests; **all green**.
- **Every guard was mutation-tested** — broken on purpose, seen to fail, seen to
  fail for the right reason, restored.
- Docker-backed tests leave **nothing behind** across two consecutive runs.
- `go build ./...`, `go vet ./...`, `gofmt -l` clean; `go test -short ./...`
  green; `tsc -b` + a `web/dist` rebuild if `web/src` changed.
- Update `docs/` + `CHANGELOG.md` for user-facing changes.
