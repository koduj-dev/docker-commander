---
name: git-workflow
description: Use BEFORE any git or release operation in this repo — committing, branching, staging, opening or editing a PR, tagging, or cutting a release. Captures the project's conventions so they aren't re-derived or skipped: PR bodies MUST follow .github/PULL_REQUEST_TEMPLATE.md, commits use Conventional-Commit style with NO Co-Authored-By / Claude trailer (and none in PR bodies either), branch before committing (never commit straight to main), the gofmt gate runs AFTER git add, web/dist is committed whenever web/src changes, CHANGELOG accumulates under [Unreleased] then becomes [x.y.z] + a link def at release, and pushing a vX.Y.Z tag triggers release.yml. Read it before touching git.
---

# Docker Commander — git & release conventions

These are **project decisions already made**. Follow them so the maintainer
doesn't have to re-specify them every time. Anything that pushes, tags, or opens
a PR is outward-facing — **confirm with the user before doing it** unless they've
already told you to proceed.

## Branching

- **Never commit directly to `main`.** Create a branch first:
  `feat/…`, `fix/…`, `docs/…`, `chore/…`, `release/vX.Y.Z`.
- One **frontend** PR in flight at a time — `web/dist` is a committed build
  artifact and parallel frontend branches conflict on it (see below).

## Commit messages

- **Conventional Commits**: `type(scope): subject`, imperative, lower-case
  subject, no trailing period. Types seen in this repo: `feat`, `fix`, `docs`,
  `chore`, `build`, `perf`, `security`, `refactor`. Scope is optional
  (`feat(mcp): …`, `docs(web): …`).
- **NO `Co-Authored-By` and NO "Generated with Claude Code" trailer** — not in
  commit messages and not in PR bodies. The maintainer keeps the history clean.
- Body (optional) explains *what* and *why*, wrapped ~72 cols.

## Pull requests

- **Always fill in `.github/PULL_REQUEST_TEMPLATE.md`** — do not invent your own
  structure. The sections are **Summary**, **Type of change**, **Checklist**,
  **Notes for reviewers**.
- Tick the **Type of change** that applies; tick **Checklist** items honestly.
  For items that don't apply, leave the box unchecked and append `— N/A (reason)`
  rather than ticking a step you didn't actually do.
- Put trade-offs, follow-ups, and any throwaway test data you created/cleaned up
  under **Notes for reviewers**. Reference screenshots for UI changes.
- No Claude trailer in the body.

## Pre-PR / pre-merge checks (run what applies to the diff)

- **Go changed** → `go test -short ./...` and `go vet ./...` must pass.
- **gofmt gate** → the CI gate only checks *tracked* files, so run it **after
  staging**: `git add -A` then `gofmt -l $(git ls-files '*.go')` must print
  nothing. (gofmt before `git add` misses newly-staged files.)
- **UI changed (`web/src`)** → `cd web && npx tsc --noEmit` type-checks, and you
  **must rebuild and commit `web/dist`** with `make ui` (it's embedded by the Go
  build so the binary works without Node). Never hand-merge a `web/dist`
  conflict — regenerate it. If the diff doesn't touch `web/src`, leave `web/dist`
  alone.
- **New/changed behaviour** → add tests (see the `feature-tests` skill).
- **User-facing change** → update `docs/` and add a `CHANGELOG.md` entry.

## CHANGELOG

- Accumulate entries under `## [Unreleased]` while developing, grouped
  **Security / Added / Changed / Fixed** (Keep a Changelog + semver).
- **At release**: rename `## [Unreleased]` → `## [x.y.z] — YYYY-MM-DD` and add a
  matching link definition at the **bottom** of the file, above the previous
  one: `[x.y.z]: https://github.com/koduj-dev/docker-commander/releases/tag/vx.y.z`.
  A version header without its link def is a broken release entry.

## Releasing

- The version is stamped into the binary from the git tag via ldflags
  (`-X main.version`). **Do not bump a version string in source files** — there
  isn't one to bump.
- Pushing a **`vX.Y.Z`** tag triggers `.github/workflows/release.yml`, which
  cross-compiles all platforms and publishes a GitHub release. So tagging is the
  publish action — **confirm before `git tag … && git push origin vX.Y.Z`**.
- Typical flow: branch → PR (template) → merge to `main` → `git tag vX.Y.Z` on
  `main` → push tag.

## Screenshots (docs)

- Regenerate the manual screenshots with the generator in
  `scripts/screenshots/` (`DC_PASS=… npm run shoot`, or `ONLY=name,…`). Its
  `node_modules` / lockfile are gitignored; the PNGs land in `docs/images/` at
  2560×1440 and are committed.
