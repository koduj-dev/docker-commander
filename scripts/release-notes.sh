#!/bin/sh
# release-notes.sh VERSION — print the CHANGELOG section for VERSION (without
# the leading "v"), followed by a link to the whole CHANGELOG, as the text of
# the GitHub release. Exits non-zero if CHANGELOG.md has no such section, so a
# release can't go out with an empty description: releases are immutable.
set -eu
ver="${1#v}"
repo="${GITHUB_REPOSITORY:-koduj-dev/docker-commander}"
body="$(awk -v v="$ver" '
  index($0, "## [" v "]") == 1 { p = 1; next }
  p && /^## \[/ { exit }
  p && !started && /^[[:space:]]*$/ { next }
  p { started = 1; print }
' "${CHANGELOG:-CHANGELOG.md}")"
if [ -z "$(printf '%s' "$body" | tr -d '[:space:]')" ]; then
  echo "CHANGELOG.md has no section for $ver" >&2
  exit 1
fi
printf '%s\n\n**Full changelog:** https://github.com/%s/blob/v%s/CHANGELOG.md\n' "$body" "$repo" "$ver"
