#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root"
[[ "$(uname -s)/$(uname -m)" == Linux/x86_64 ]] || {
  echo "This reference supports native Linux x86_64 hosts only." >&2
  exit 2
}
[[ "$(docker info --format '{{.OSType}}/{{.Architecture}}')" == linux/x86_64 ]] || {
  echo "A native linux/amd64 Docker daemon with BuildKit is required." >&2
  exit 2
}
mkdir -p .runner-reference/build .runner-reference/go
rm -f .runner-reference/image-id .runner-reference/image-config-id .runner-reference/published-reference
trap 'rm -rf "$root/.runner-reference/context"' EXIT

# Only tracked local inputs: never inherit node_modules, web/dist/assets, or
# ambient compiler flags. Verify hydrated LFS bytes against committed pointers.
python3 - <<'PY'
import hashlib
import re
import shutil
import subprocess
from pathlib import Path

context = Path(".runner-reference/context")
if context.exists():
    shutil.rmtree(context)
context.mkdir()
paths = subprocess.check_output(["git", "ls-files", "-z"]).decode().split("\0")
for name in filter(None, paths):
    source = Path(name)
    if name.startswith("internal/embedded/") and re.search(r"linux.*(?:amd64|x64).*\.(?:zst|tgz)$", name):
        pointer = subprocess.check_output(["git", "show", f"HEAD:{name}"]).decode()
        expected = re.search(r"^oid sha256:([a-f0-9]{64})$", pointer, re.M)
        with source.open("rb") as payload:
            actual = hashlib.file_digest(payload, "sha256").hexdigest()
        if not expected or actual != expected[1]:
            raise SystemExit(f"Unhydrated or corrupt LFS payload: {name}; run git lfs pull before building")
    destination = context / name
    destination.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(source, destination)
PY

context="$root/.runner-reference/context"
# Pin the complete Node/npm distribution, not the host's Node installation.
docker run --rm --platform linux/amd64 \
  --mount "type=bind,src=$context,dst=/src" --workdir /src/web \
  --env TMPDIR=/src/.build --env npm_config_cache=/src/.npm \
  node:22.14.0-bookworm-slim@sha256:745403dc46b5ab4c998502b07a12cbf020cf2c30645427a68ec0718f02d647de \
  sh -c 'mkdir -p /src/.build && npm ci --no-audit --no-fund && npm run build'
rm -rf "$context/web/node_modules" "$context/.npm" "$context/.build"

(
  cd "$context"
  export GOENV=off GOTOOLCHAIN=go1.26.4 GOFLAGS= GOEXPERIMENT= GOWORK=off
  export CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64=v1 GOFIPS140=off
  export GOTMPDIR="$root/.runner-reference/go" TMPDIR="$root/.runner-reference/go"
  go version
  go mod verify
  go build -mod=readonly -trimpath -buildvcs=false \
    -ldflags '-s -w -X main.version=runner-reference' \
    -o "$root/.runner-reference/build/waza" ./cmd/waza
)
rm -rf "$context"
sha256sum .runner-reference/build/waza > .runner-reference/binary.sha256
epoch="$(git log -1 --format=%ct)"
touch -d "@$epoch" .runner-reference/build/waza
docker buildx build --platform linux/amd64 --provenance=false \
  --output type=docker,rewrite-timestamp=true,dest=- \
  --tag waza-runner-reference:local \
  --build-arg "SOURCE_DATE_EPOCH=$epoch" \
  --build-arg "REVISION=$(git rev-parse HEAD)" \
  --file examples/runner/Dockerfile --network=none \
  --iidfile .runner-reference/image-config-id "$@" .runner-reference/build | docker load --quiet
# Docker's containerd image store addresses images by manifest, while older
# daemons use config IDs. Keep the runnable ID separate from the OCI config ID.
docker image inspect --format '{{.Id}}' waza-runner-reference:local > .runner-reference/image-id
rm -f .runner-reference/build/waza
printf 'Local test image (not a published reference): %s\n' "$(cat .runner-reference/image-id)"
