#!/usr/bin/env bash
set -euo pipefail

tag="$1"
remote_tags="$(git ls-remote --exit-code origin "refs/tags/$tag" "refs/tags/$tag^{}")"
tag_sha="$(printf '%s\n' "$remote_tags" | awk -v ref="refs/tags/$tag" '
  $2 == ref {raw = $1; raws++}
  $2 == ref "^{}" {peeled = $1; peeleds++}
  END {
    if (raws != 1 || peeleds > 1) exit 1
    print peeleds ? peeled : raw
  }')"
if [ "$tag_sha" != "$GITHUB_SHA" ]; then
  printf 'Release tag %s does not point to the validated source %s; existing tags were not changed.\n' "$tag" "$GITHUB_SHA" >&2
  exit 1
fi
