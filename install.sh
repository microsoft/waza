#!/bin/bash
set -euo pipefail

# install.sh — Download and install the latest waza binary from GitHub.
# Usage: curl -fsSL https://raw.githubusercontent.com/spboyer/waza/main/install.sh | bash

REPO="spboyer/waza"
BINARY_NAME="waza"

# Global so the EXIT trap can access it after main() returns
tmpdir=""

# Detect OS
detect_os() {
  local os
  os="$(uname -s)"
  case "$os" in
    Linux*)  echo "linux" ;;
    Darwin*) echo "darwin" ;;
    MINGW*|MSYS*|CYGWIN*) echo "windows" ;;
    *) echo "Unsupported OS: $os" >&2; exit 1 ;;
  esac
}

# Detect architecture
detect_arch() {
  local arch
  arch="$(uname -m)"
  case "$arch" in
    x86_64|amd64)  echo "amd64" ;;
    aarch64|arm64)  echo "arm64" ;;
    *) echo "Unsupported architecture: $arch" >&2; exit 1 ;;
  esac
}

# Determine install directory
install_dir() {
  if [ -w /usr/local/bin ]; then
    echo "/usr/local/bin"
  else
    local dir="$HOME/bin"
    mkdir -p "$dir"
    echo "$dir"
  fi
}

main() {
  local os arch version tag asset_name install_path

  os="$(detect_os)"
  arch="$(detect_arch)"

  echo "Detected platform: ${os}/${arch}"

  # Get latest binary release tag (filter for v* tags; skip azd extension releases)
  tag="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases" \
    | grep '"tag_name": "v' | head -1 | cut -d'"' -f4)"

  if [ -z "$tag" ]; then
    echo "Error: could not determine latest release." >&2
    exit 1
  fi

  version="${tag#v}"
  echo "Latest version: ${version} (${tag})"

  asset_name="${BINARY_NAME}-${os}-${arch}"
  if [ "$os" = "windows" ]; then
    asset_name="${asset_name}.exe"
  fi

  tmpdir="$(mktemp -d)"
  trap '[ -n "$tmpdir" ] && rm -rf "$tmpdir"' EXIT

  echo "Downloading ${asset_name}..."
  if ! curl -fSL -o "${tmpdir}/${asset_name}" \
    "https://github.com/${REPO}/releases/download/${tag}/${asset_name}"; then
    echo "Error: Failed to download '${asset_name}' from release '${tag}'." >&2
    echo "Check available assets at: https://github.com/${REPO}/releases/tag/${tag}" >&2
    exit 1
  fi

  echo "Downloading checksums..."
  curl -fSL -o "${tmpdir}/checksums.txt" \
    "https://github.com/${REPO}/releases/download/${tag}/checksums.txt"

  echo "Verifying checksum..."
  if command -v sha256sum >/dev/null 2>&1; then
    (cd "$tmpdir" && grep "${asset_name}" checksums.txt | sha256sum -c --status)
  elif command -v shasum >/dev/null 2>&1; then
    (cd "$tmpdir" && grep "${asset_name}" checksums.txt | shasum -a 256 -c --status)
  else
    echo "Warning: no sha256sum or shasum found, skipping checksum verification"
  fi
  echo "Checksum verified ✓"

  install_path="$(install_dir)"
  local dest="${install_path}/${BINARY_NAME}"
  if [ "$os" = "windows" ]; then
    dest="${dest}.exe"
  fi

  cp "${tmpdir}/${asset_name}" "$dest"
  chmod +x "$dest"

  echo ""
  echo "Installed ${BINARY_NAME} ${version} to ${dest}"

  # Hint if ~/bin is not on PATH
  if [ "$install_path" = "$HOME/bin" ]; then
    case ":$PATH:" in
      *":$HOME/bin:"*) ;;
      *) echo "Note: Add ~/bin to your PATH: export PATH=\"\$HOME/bin:\$PATH\"" ;;
    esac
  fi
}

main "$@"
