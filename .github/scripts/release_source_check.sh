#!/usr/bin/env bash
set -euo pipefail

source_sha="$(git rev-parse HEAD)"
test "$source_sha" = "$GITHUB_SHA"
git ls-files --error-unmatch web/dist/index.html web/dist/favicon.svg version.txt extension.yaml >/dev/null
source_status="$(git status --porcelain --untracked-files=normal)"
if [ -n "$source_status" ]; then
  printf 'Release source is not clean after the dashboard build. Commit matching assets and target metadata before tagging:\n%s\n' "$source_status" >&2
  exit 1
fi
