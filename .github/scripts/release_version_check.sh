#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C

unsupported() {
  echo "Unsupported release version: require uint64 major.minor.patch and optional dot-separated alphanumeric prerelease without leading zeroes in numeric identifiers; no build metadata; at most 127 bytes before the v prefix." >&2
  exit 1
}

# Keep the existing candidate/tag grammar's intersection. The runner publisher
# uses v${version} verbatim as an OCI tag: no build metadata, at most 128 bytes.
if [ "$#" -ne 1 ] ||
  [[ ! "$1" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[A-Za-z0-9]+(\.[A-Za-z0-9]+)*)?$ ]] ||
  [ "${#1}" -gt 127 ]; then
  unsupported
fi

core="${1%%-*}"
IFS=. read -r -a components <<< "$core"
for component in "${components[@]}"; do
  if [[ ${#component} -gt 20 || (${#component} -eq 20 && "$component" > 18446744073709551615) ]]; then
    unsupported
  fi
done

if [[ "$1" == *-* ]]; then
  prerelease="${1#*-}"
  IFS=. read -r -a identifiers <<< "$prerelease"
  for identifier in "${identifiers[@]}"; do
    if [[ "$identifier" =~ ^0[0-9]+$ ]]; then
      unsupported
    fi
  done
fi
