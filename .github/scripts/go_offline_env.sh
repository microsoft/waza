#!/usr/bin/env bash
set -euo pipefail

directory="$(cd "${RUNNER_TEMP:?RUNNER_TEMP must name an existing temporary directory}" && pwd -P)"
guard="$(mktemp "$directory/offline-copilot.XXXXXX")"
cat > "$guard" <<'EOF'
#!/bin/sh
: > "$0.invoked"
printf 'offline test gate: Copilot runtime disabled\n' >&2
exit 64
EOF
chmod +x "$guard"
status=0
"$guard" --version >&2 || status=$?
if [ "$status" -ne 64 ] || [ ! -f "$guard.invoked" ]; then
  echo "Rejecting Copilot runtime guard failed its launch proof." >&2
  exit 1
fi
rm "$guard.invoked"
printf 'ENABLE_COPILOT_TESTS=false\nWAZA_MCP_RUNTIME_TEST=false\nWAZA_EVIDENCE_JUDGE_RUNTIME_TEST=false\nWAZA_RESPONDER_RUNTIME_TEST=false\nNO_COLOR=1\nCOPILOT_CLI_PATH=%s\n' "$guard"
