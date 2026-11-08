#!/usr/bin/env bash
set -euo pipefail

build_info="$(go version -m "$1")"
revision="$(printf '%s\n' "$build_info" | awk '$1 == "build" && $2 ~ /^vcs.revision=/ {sub(/^vcs.revision=/, "", $2); print $2}')"
modified="$(printf '%s\n' "$build_info" | awk '$1 == "build" && $2 ~ /^vcs.modified=/ {sub(/^vcs.modified=/, "", $2); print $2}')"
if [ "$revision" != "$GITHUB_SHA" ] || [ "$modified" != "false" ]; then
  printf 'Release binary %s lacks clean exact-source VCS metadata:\n%s\n' "$1" "$build_info" >&2
  exit 1
fi
