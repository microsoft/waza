#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root"
image="$(cat .runner-reference/image-id)"
[[ "$image" =~ ^sha256:[0-9a-f]{64}$ ]] || {
  echo "Build the reference first; only an immutable image ID is accepted." >&2
  exit 2
}
mkdir -p .runner-reference/output

# No network, host project mounts, credentials, queue or scheduling integration.
# This bounded example is not a strong-isolation claim.
docker run --rm --interactive --network=none --read-only \
  --tmpfs /work:rw,noexec,nosuid,size=32m,uid=65532,gid=65532,mode=0700 \
  "$image" < examples/runner/request.json > .runner-reference/output/unit.runner.json
docker run --rm --interactive --network=none --read-only --entrypoint /waza \
  "$image" runner validate-result < .runner-reference/output/unit.runner.json
python3 - <<'PY'
import json
from pathlib import Path

output = Path(".runner-reference/output/unit.runner.json").read_text()
result = json.loads(output)  # Rejects a second JSON document.
vector = json.loads(Path("internal/runnercontract/testdata/identity.json").read_text())
assert result["outcome"] == "success" and result["exit_code"] == 0
assert result["identity"] == vector
assert result["resolved"] == {"shard": "shard-a", "task": "selected", "trial": 2, "attempt": 2}
assert result["observation"]["run_number"] == 2
assert result["observation"]["attempts"] == 2
assert result["adjacent_work_selected"] is False
print("Reference passed: one exact unit, frozen identity, schema-valid terminal envelope.")
PY
