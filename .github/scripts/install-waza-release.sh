#!/usr/bin/env bash
set -euo pipefail

version="${1:-}"
if [ "$#" -ne 1 ] || [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "::error::waza-version must be an exact numeric release such as 0.41.0" >&2
  exit 1
fi

install_dir="${RUNNER_TEMP:?RUNNER_TEMP must be set}/waza-bin"
mkdir -p "$install_dir"
curl -fL --retry 3 -o "$install_dir/waza-linux-amd64" \
  "https://github.com/microsoft/waza/releases/download/v${version}/waza-linux-amd64"
curl -fL --retry 3 -o "$install_dir/checksums.txt" \
  "https://github.com/microsoft/waza/releases/download/v${version}/checksums.txt"
(
  cd "$install_dir"
  grep ' waza-linux-amd64$' checksums.txt | sha256sum --check -
)
mv "$install_dir/waza-linux-amd64" "$install_dir/waza"
chmod +x "$install_dir/waza"
echo "$install_dir" >> "${GITHUB_PATH:?GITHUB_PATH must be set}"
