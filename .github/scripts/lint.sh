#!/usr/bin/env bash
set -euo pipefail

if ! golangci-lint version | grep -Eq '^golangci-lint has version 2[.]10[.]1( |$)'; then
  echo "Expected golangci-lint v2.10.1; update the workflow version and pinned schema together." >&2
  exit 1
fi

schema="$(mktemp "${RUNNER_TEMP:?RUNNER_TEMP must be set}/golangci-schema.XXXXXX")"
trap 'rm -f "$schema"' EXIT

# Validate against the release schema without the verifier's two-second HTTP timeout.
curl --fail --show-error --silent --location --retry 2 --connect-timeout 10 --max-time 60 \
  --output "$schema" \
  https://raw.githubusercontent.com/golangci/golangci-lint/5d1e709b7be35cb2025444e19de266b056b7b7ee/jsonschema/golangci.jsonschema.json
printf '%s  %s\n' 692e8da79dc1f110a4a4f3bb841b26ed44e461d3503678eaaae90ae8876e3403 "$schema" | sha256sum --check
golangci-lint config verify --config .golangci.yml --schema "$schema"
golangci-lint run --config .golangci.yml --path-mode=abs --timeout=5m
