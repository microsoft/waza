#!/usr/bin/env bash
set -euo pipefail

directory="$(cd "${RUNNER_TEMP:?RUNNER_TEMP must name an existing temporary directory}" && pwd -P)"
if [ "${RUNNER_OS:-}" = Windows ]; then
  guard_dir="$(mktemp -d "$directory/offline-copilot.XXXXXX")"
  guard="$guard_dir/offline-copilot.exe"
  cat > "$guard_dir/main.go" <<'EOF'
package main

import (
	"fmt"
	"os"
)

func main() {
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(65)
	}
	if err := os.WriteFile(executable+".invoked", nil, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(74)
	}
	fmt.Fprintln(os.Stderr, "offline test gate: Copilot runtime disabled")
	os.Exit(64)
}
EOF
  go build -o "$guard" "$guard_dir/main.go"
  rm "$guard_dir/main.go"
else
  guard="$(mktemp "$directory/offline-copilot.XXXXXX")"
  cat > "$guard" <<'EOF'
#!/bin/sh
: > "$0.invoked"
printf 'offline test gate: Copilot runtime disabled\n' >&2
exit 64
EOF
  chmod +x "$guard"
fi
status=0
"$guard" --version >&2 || status=$?
if [ "$status" -ne 64 ] || [ ! -f "$guard.invoked" ]; then
  echo "Rejecting Copilot runtime guard failed its launch proof." >&2
  exit 1
fi
rm "$guard.invoked"
if [ "${RUNNER_OS:-}" = Windows ]; then
  guard="$(cygpath -m "$guard")"
fi
printf 'ENABLE_COPILOT_TESTS=false\nWAZA_MCP_RUNTIME_TEST=false\nWAZA_EVIDENCE_JUDGE_RUNTIME_TEST=false\nWAZA_RESPONDER_RUNTIME_TEST=false\nNO_COLOR=1\nCOPILOT_CLI_PATH=%s\n' "$guard"
