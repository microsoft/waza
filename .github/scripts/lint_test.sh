#!/usr/bin/env bash
set -euo pipefail

helper="$(cd "$(dirname "$0")" && pwd)/lint.sh"
test_dir="$(mktemp -d "${TMPDIR:-/tmp}/waza-lint-test.XXXXXX")"
trap 'rm -rf "$test_dir"' EXIT
mkdir "$test_dir/bin" "$test_dir/tmp"

# Mock only external commands to check sequencing, failure propagation, and cleanup.
cat > "$test_dir/bin/command" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
name="${0##*/}"
printf '%s\n' "$name" >> "$CALLS"
case "$name" in
  golangci-lint)
    case "$1" in
      version)
        if [ "$CASE" = version-error ]; then exit 9; fi
        version=2.10.1
        if [ "$CASE" = version-mismatch ]; then version=2.12.2; fi
        echo "golangci-lint has version $version built with go1.26.0"
        ;;
      config)
        case "$*" in
          "config verify --config .golangci.yml --schema "*) ;;
          *) echo "Unexpected configuration command: $*" >&2; exit 99 ;;
        esac
        if [ "$CASE" = config-error ]; then exit 1; fi
        ;;
      run)
        if [ "$*" != "run --config .golangci.yml --path-mode=abs --timeout=5m" ]; then
          echo "Unexpected lint command: $*" >&2
          exit 99
        fi
        if [ "$CASE" = lint-error ]; then exit 3; fi
        ;;
      *) echo "Unexpected linter command: $*" >&2; exit 99 ;;
    esac
    ;;
  curl) if [ "$CASE" = download-error ]; then exit 22; fi ;;
  sha256sum)
    cat > /dev/null
    if [ "$CASE" = checksum-error ]; then exit 1; fi
    ;;
  *) echo "Unexpected executable: $name" >&2; exit 99 ;;
esac
EOF
chmod +x "$test_dir/bin/command"
for name in golangci-lint curl sha256sum; do
  ln -s command "$test_dir/bin/$name"
done

while IFS='|' read -r scenario expected_status expected_calls; do
  : > "$test_dir/calls"
  status=0
  runner_temp="$test_dir/tmp"
  if [ "$scenario" = missing-temp ]; then runner_temp=""; fi
  if [ "$scenario" = unavailable-temp ]; then runner_temp="$test_dir/missing"; fi
  PATH="$test_dir/bin:$PATH" RUNNER_TEMP="$runner_temp" \
    CASE="$scenario" CALLS="$test_dir/calls" \
    bash "$helper" > "$test_dir/output" 2>&1 || status=$?
  calls="$(paste -sd, "$test_dir/calls")"
  if [ "$status" -ne "$expected_status" ] || [ "$calls" != "$expected_calls" ]; then
    cat "$test_dir/output" >&2
    echo "$scenario: expected $expected_status/$expected_calls, got $status/$calls" >&2
    exit 1
  fi
  remaining="$(find "$test_dir/tmp" -mindepth 1 -print)"
  if [ -n "$remaining" ]; then
    echo "$scenario: schema temporary file was not cleaned up: $remaining" >&2
    exit 1
  fi
  echo "$scenario: PASS"
done <<'EOF'
success|0|golangci-lint,curl,sha256sum,golangci-lint,golangci-lint
version-mismatch|1|golangci-lint
version-error|1|golangci-lint
missing-temp|1|golangci-lint
unavailable-temp|1|golangci-lint
download-error|22|golangci-lint,curl
checksum-error|1|golangci-lint,curl,sha256sum
config-error|1|golangci-lint,curl,sha256sum,golangci-lint
lint-error|3|golangci-lint,curl,sha256sum,golangci-lint,golangci-lint
EOF
