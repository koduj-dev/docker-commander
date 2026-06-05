<!-- Thanks for contributing! Please fill this in and tick the checklist. -->

## Summary

<!-- What does this PR do and why? Link any related issue (e.g. "Closes #123"). -->

## Type of change

- [ ] Bug fix
- [ ] New feature
- [ ] Docs only
- [ ] Refactor / chore

## Checklist

- [ ] `go test -short ./...` and `go vet ./...` pass
- [ ] `gofmt` gate is clean (`gofmt -l $(git ls-files '*.go')` after staging)
- [ ] Frontend type-checks (`cd web && npx tsc --noEmit`) — if the UI changed
- [ ] Rebuilt and committed `web/dist` — if anything under `web/src` changed
- [ ] Added/updated tests for the change
- [ ] Updated `docs/` and added a `CHANGELOG.md` entry for user-facing changes

## Notes for reviewers

<!-- Anything worth calling out: trade-offs, follow-ups, screenshots for UI changes. -->
