#!/bin/bash
set -euo pipefail

# install.sh — Download and install the latest waza binary from GitHub.
# Usage: curl -fsSL https://raw.githubusercontent.com/microsoft/waza/main/install.sh | bash

REPO="microsoft/waza"
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

# Detect whether Linux is actually WSL. In that case the Linux binary is the
# right install target for this shell, but users often expected native Windows.
is_wsl() {
  [ -r /proc/sys/kernel/osrelease ] && grep -qiE 'microsoft|wsl' /proc/sys/kernel/osrelease
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
  if [ "$os" = "linux" ] && is_wsl; then
    echo "Note: Detected WSL; installing the Linux binary inside WSL."
    echo "For native Windows, download waza-windows-${arch}.exe from https://github.com/${REPO}/releases/latest."
  fi

  # Get latest stable binary release tag.
  tag="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
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
  # Try shasum first (native on macOS and supports -c flag)
  # Fall back to sha256sum (Linux, but only if it supports -c flag)
  if command -v shasum >/dev/null 2>&1; then
    (cd "$tmpdir" && grep "${asset_name}" checksums.txt | shasum -a 256 -c --status)
  elif command -v sha256sum >/dev/null 2>&1 && sha256sum --help 2>&1 | grep -q -- '-c'; then
    (cd "$tmpdir" && grep "${asset_name}" checksums.txt | sha256sum -c --status)
  else
    echo "Error: No compatible checksum utility found (sha256sum or shasum)" >&2
    exit 1
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
