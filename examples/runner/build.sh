#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root"
mkdir -p .runner-reference/build .runner-reference/go

# Override only intentionally; compiler changes produce a different binary/image.
export GOTOOLCHAIN="${GOTOOLCHAIN:-go1.26.4}"
export GOTMPDIR="$root/.runner-reference/go"
export TMPDIR="$root/.runner-reference/go"
CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false \
  -ldflags '-s -w -X main.version=runner-reference' \
  -o .runner-reference/build/waza ./cmd/waza
sha256sum .runner-reference/build/waza > .runner-reference/binary.sha256
docker build --quiet --network=none --file examples/runner/Dockerfile \
  --iidfile .runner-reference/image-id .runner-reference/build
printf 'Pinned local image: %s\n' "$(cat .runner-reference/image-id)"
