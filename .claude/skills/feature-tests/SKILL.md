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
- `go build ./...`, `go vet ./...`, `gofmt -l` clean; `go test -short ./...`
  green; `tsc -b` + a `web/dist` rebuild if `web/src` changed.
- Update `docs/` + `CHANGELOG.md` for user-facing changes.
