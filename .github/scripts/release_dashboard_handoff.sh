#!/usr/bin/env bash
set -euo pipefail

mode="${1:?require pack, install or verify-installed}"
case "$mode" in
  pack|install|verify-installed) ;;
  *) echo "Unsupported dashboard handoff mode: $mode" >&2; exit 1 ;;
esac
artifact="${RELEASE_DASHBOARD_ARTIFACT:?require external dashboard artifact path}"

# Equality with an artifact is not admission of the checkout that embeds it.
bash .github/scripts/release_source_check.sh
checkout_sha="$(git rev-parse HEAD)"
arguments=(
  "$mode" --input .
  --repository "${GITHUB_REPOSITORY:?}"
  --source-sha "${GITHUB_SHA:?}" --checkout-sha "$checkout_sha"
  --run-id "${GITHUB_RUN_ID:?}" --run-attempt "${GITHUB_RUN_ATTEMPT:?}"
)
if [ "$mode" = pack ]; then
  GOOS= GOARCH= GOPROXY=off GOSUMDB=off go run ./.github/scripts/release-dashboard-check
  arguments+=(--destination "$artifact")
else
  arguments[2]="$artifact"
  arguments+=(--workspace . --manifest-sha256 "${RELEASE_DASHBOARD_MANIFEST_SHA256:?require independently carried manifest digest}")
fi
result="$(GOOS= GOARCH= GOPROXY=off GOSUMDB=off go run ./.github/scripts/release-assets-helper "${arguments[@]}")"
GOOS= GOARCH= GOPROXY=off GOSUMDB=off go run ./.github/scripts/release-dashboard-check
bash .github/scripts/release_source_check.sh
# Do not expose a producer digest until semantic and final source checks pass.
printf '%s\n' "$result"
