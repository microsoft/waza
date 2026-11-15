#!/usr/bin/env bash
set -euo pipefail

scripts="$(cd "$(dirname "$0")" && pwd)"
repository="$(cd "$scripts/../.." && pwd)"
python3 -B "$scripts/go_remaining_shard_plan_test.py"
python3 -B "$scripts/go_remaining_shard_verify_test.py"

scratch="$repository/.ci-go-remaining-script-test-${GITHUB_RUN_ID:-local}-${BASHPID}"
if [ -e "$scratch" ]; then
  echo "Refusing to overwrite existing test workspace: $scratch" >&2
  exit 1
fi
mkdir -p "$scratch/bin" "$scratch/reports"
if [ "${KEEP_TEST_SCRATCH:-false}" = true ]; then
  echo "Keeping remaining-shard test workspace: $scratch"
else
  trap 'rm -rf "$scratch"' EXIT
fi

cat > "$scratch/bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

if [ "$*" = "version" ]; then
  echo "go version go1.26.4 linux/amd64"
  exit 0
fi
if [ "$*" = "list ./..." ]; then
  printf '%s\n' example/pkg-0 example/pkg-1 example/pkg-2 example/pkg-3 github.com/microsoft/waza/internal/controlledcomparison
  exit 0
fi
if [ "$1" != "test" ]; then
  echo "Unexpected Go invocation: $*" >&2
  exit 88
fi

phase=execution
profile=""
packages=()
for argument in "$@"; do
  if [ "$argument" = "-list" ]; then phase=discovery; fi
  if [[ "$argument" = -coverprofile=* ]]; then profile="${argument#-coverprofile=}"; fi
  if [[ "$argument" = example/pkg-* ]]; then packages+=("$argument"); fi
done
if [ "${#packages[@]}" -ne 4 ] && [ "$phase" = discovery ]; then
  echo "Discovery did not receive all four remaining packages." >&2
  exit 88
fi

for package in "${packages[@]}"; do
  printf '{"Action":"start","Package":"%s"}\n' "$package"
  if [ "$phase" = discovery ]; then
    printf '{"Action":"output","Package":"%s","Output":"TestObserved\\n"}\n' "$package"
    printf '{"Action":"pass","Package":"%s","Elapsed":0.001}\n' "$package"
  else
    printf '{"Action":"run","Package":"%s","Test":"TestObserved"}\n' "$package"
    if [ "${FAKE_EXECUTION_EXIT:-0}" -eq 0 ]; then
      printf '{"Action":"pass","Package":"%s","Test":"TestObserved","Elapsed":0.001}\n' "$package"
      printf '{"Action":"pass","Package":"%s","Elapsed":0.002}\n' "$package"
    else
      printf '{"Action":"fail","Package":"%s","Test":"TestObserved","Elapsed":0.001}\n' "$package"
      printf '{"Action":"fail","Package":"%s","Elapsed":0.002}\n' "$package"
    fi
  fi
done
if [ "$phase" = execution ]; then
  if [ -z "$profile" ]; then
    echo "Execution omitted its coverage profile." >&2
    exit 88
  fi
  printf 'mode: atomic\n' > "$profile"
  for package in "${packages[@]}"; do
    printf '%s/test.go:1.1,2.1 1 1\n' "$package" >> "$profile"
  done
  exit "${FAKE_EXECUTION_EXIT:-0}"
fi
exit "${FAKE_DISCOVERY_EXIT:-0}"
EOF
chmod +x "$scratch/bin/go"

export PATH="$scratch/bin:$PATH"
export GITHUB_REPOSITORY=microsoft/waza
export GITHUB_WORKFLOW="Go Build and Test"
export GITHUB_EVENT_NAME=push
export GITHUB_REF=refs/heads/main
export GITHUB_RUN_ID=12345
export GITHUB_RUN_ATTEMPT=1
export GITHUB_JOB=remaining
export GITHUB_SHA="$(git -C "$repository" rev-parse HEAD)"
export RUNNER_OS=Linux
export RUNNER_ARCH=X64
export GO_CACHE_HIT=false
export NODE_CACHE_HIT=false

for shard in 0 1 2 3; do
  bash "$scripts/go_remaining_test_shard.sh" "$shard" "$scratch/reports/coverage-remaining-$shard.txt"
done
python3 -B "$scripts/go_remaining_shard_verify.py" \
  "$scratch/reports" "$scratch/merged-coverage.txt" "$scratch/verification.json"

expect_status() {
  local expected="$1"
  local actual=0
  shift
  "$@" > "$scratch/failure.log" 2>&1 || actual=$?
  if [ "$actual" -ne "$expected" ]; then
    cat "$scratch/failure.log" >&2
    echo "Expected status $expected, got $actual: $*" >&2
    exit 1
  fi
}
expect_status 7 env FAKE_DISCOVERY_EXIT=7 \
  bash "$scripts/go_remaining_test_shard.sh" 0 "$scratch/discovery-failure.txt"
expect_status 9 env FAKE_EXECUTION_EXIT=9 \
  bash "$scripts/go_remaining_test_shard.sh" 0 "$scratch/execution-failure.txt"

echo "Remaining-shard planning, exact-once selection, failure handling, and coverage merge: PASS"
