#!/usr/bin/env bash
set -euo pipefail
if [ -n "${FAIL_COLLECTOR:-}" ] && [ "${2:-}" = "$FAIL_COLLECTOR" ]; then
  cat > /dev/null
  echo "controlled collector failure" >&2
  exit 11
fi
if [ -n "${FAIL_FINALIZER:-}" ] && [ "${2:-}" = "$FAIL_FINALIZER" ]; then
  echo "controlled finalization failure" >&2
  exit 13
fi
exec "$REAL_NODE" "$@"
