#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root"

before="$(cat .runner-reference/image-id)"
bash examples/runner/smoke.sh --local
# Recompile without layer-cache reuse; image/config timestamps are source-bound.
bash examples/runner/build.sh --no-cache
[[ "$(cat .runner-reference/image-id)" == "$before" ]] || {
  echo "Reference rebuild changed image bytes." >&2
  exit 1
}
bash examples/runner/smoke.sh --local
for reference in ghcr.io/microsoft/waza-runner-reference:latest \
  ghcr.io/microsoft/waza-runner-reference:sha-deadbeef "$before" \
  ghcr.io/microsoft/waza-runner-reference@sha256:bad ''; do
  if bash examples/runner/smoke.sh "$reference"; then
    echo "Floating/malformed reference accepted" >&2
    exit 1
  else
    [[ "$?" == 2 ]] || exit 1
  fi
done
python3 -m unittest discover -s examples/runner -p '*_test.py'
