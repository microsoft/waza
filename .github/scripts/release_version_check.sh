#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C

# Keep the existing candidate/tag grammar's intersection. The runner publisher
# uses v${version} verbatim as an OCI tag: no build metadata, at most 128 bytes.
if [ "$#" -ne 1 ] ||
  [[ ! "$1" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[A-Za-z0-9]+(\.[A-Za-z0-9]+)*)?$ ]] ||
  [ "${#1}" -gt 127 ]; then
  echo "Unsupported release version: require major.minor.patch with optional dot-separated alphanumeric prerelease, no build metadata, and at most 127 bytes before the v prefix." >&2
  exit 1
fi
