---
name: git-workflow
description: Use BEFORE any git or release operation in this repo — committing, branching, staging, opening or editing a PR, tagging, or cutting a release. Captures the project's conventions so they aren't re-derived or skipped: dev-cycle PRs target the long-lived `release/vX.Y.Z` branch (not `main`) so `main` only ever holds what's shipped, and only the `release/vX.Y.Z → main` PR at ship time actually touches `main`; commit subjects start with a bracketed action (`[add]`, `[fix]`, `[docs]`) and stay terse because they feed changelog generation, with NO Co-Authored-By / Claude trailer (and none in PR bodies either); PR bodies MUST follow .github/PULL_REQUEST_TEMPLATE.md; the gofmt gate runs AFTER git add; web/dist is committed whenever web/src changes; CHANGELOG accumulates under [Unreleased] then becomes [x.y.z] + a link def at release; pushing a vX.Y.Z tag on main triggers release.yml. Read it before touching git.
---

# Docker Commander — git & release conventions

These are **project decisions already made**. Follow them so the maintainer
doesn't have to re-specify them every time. Anything that pushes, tags, or opens
a PR is outward-facing — **confirm with the user before doing it** unless they've
already told you to proceed.

## Branching

- **Never commit directly to `main`.** Create a branch first:
  `feat/…`, `fix/…`, `docs/…`, `chore/…`.
- One **frontend** PR in flight at a time — `web/dist` is a committed build
  artifact and parallel frontend branches conflict on it (see below).

## Release branches (the dev cycle lives here, not on `main`)

`main` only ever holds what's actually been released — nothing lands there
mid-cycle. All work for the next version accumulates on a long-lived
**`release/vX.Y.Z`** branch instead, so `main` never carries half-finished or
not-yet-decided work, and anyone who wants to try the next version early can
just check out that branch.

- **Starting a cycle**: cut `release/vX.Y.Z` from `main` (`X.Y.Z` = the version
  it's aiming for — bump from the last release, adjust if scope changes).
- **During the cycle**: every `feat/…`/`fix/…`/`docs/…`/`chore/…` PR targets
  `release/vX.Y.Z` as its base, **not** `main`. Everything else in this doc
  (commit style, PR template, pre-PR checks, `CHANGELOG.md` under
  `[Unreleased]`) applies unchanged — only the PR base branch changes.
- **Shipping**: on `release/vX.Y.Z`, stamp the CHANGELOG (`[Unreleased]` →
  `[x.y.z] — YYYY-MM-DD` + link def, see below), open a PR
  `release/vX.Y.Z` → `main`, merge it, then tag `vX.Y.Z` **on `main`** and
  push the tag (triggers `release.yml` — confirm first, see Releasing below).
- **Next cycle**: delete the shipped `release/vX.Y.Z` branch, cut a fresh
  `release/vX.Y.next` from the new `main`.
- **Hotfix to an already-shipped version**: bypass the release branch — branch
  from `main`, PR straight into `main`, tag directly. Don't route an urgent
  fix through whatever's mid-flight on the current release branch.
- No GitHub branch-protection rules exist on `main` (checked via
  `gh api repos/koduj-dev/docker-commander/branches/main/protection` → 404),
  so nothing blocks this merge pattern technically — the discipline is
  procedural, not enforced by the platform.

## Commit messages

- **Action in square brackets first, then as short a subject as possible**:
  `[add] preview_deploy for MCP`, `[fix] scope single-alert ack to the alert's host`,
  `[docs] split NEXT.md into roadmap, gotchas and dev environment`. Actions in use:
  `[add]`, `[fix]`, `[docs]`, `[wip]`, `[remove]`, `[revert]`.
  Commit subjects feed automatic changelog generation, which is why they are
  uniform and terse — no essays in the subject.
- Older history uses Conventional Commits (`feat(mcp): …`) and Dependabot still
  opens `build(deps): …` PRs. Don't "fix" those; write new commits in the bracket
  style.
- **NO `Co-Authored-By` and NO "Generated with Claude Code" trailer** — not in
  commit messages and not in PR bodies. The maintainer keeps the history clean.
- Body (optional) explains *what* and *why*, wrapped ~72 cols.

## Pull requests

- **The PR title is the commit subject**, same bracketed-action style
  (`[add] MCP control rate limit + remote-host projects`) — merged PRs become the
  history the changelog is generated from.
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
- Typical flow: `release/vX.Y.Z` branch accumulates PRs for the cycle → stamp
  CHANGELOG → PR `release/vX.Y.Z` → `main` (template) → merge → `git tag
  vX.Y.Z` on `main` → push tag. (Hotfixes to an already-shipped version skip
  the release branch: branch from `main`, PR straight into `main`, tag.)

## Screenshots (docs)

- Regenerate the manual screenshots with the generator in
  `scripts/screenshots/` (`DC_PASS=… npm run shoot`, or `ONLY=name,…`). Its
  `node_modules` / lockfile are gitignored; the PNGs land in `docs/images/` at
  2560×1440 and are committed.
