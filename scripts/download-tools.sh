#!/usr/bin/env bash
set -eo pipefail

# Download fd, rg, rtk binaries, gzip-compressed into internal/embedded/binaries/.
#
# Usage:
#   ./scripts/download-tools.sh                                # current platform (dev)
#   ./scripts/download-tools.sh --goos linux --goarch amd64    # cross-platform (release)

BINARIES_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/internal/embedded/binaries"
mkdir -p "$BINARIES_DIR"

FD_VERSION="${FD_VERSION:-10.4.2}"
RG_VERSION="${RG_VERSION:-15.1.0}"
RTK_VERSION="${RTK_VERSION:-0.30.0}"

GOOS="" GOARCH=""
while [[ $# -gt 0 ]]; do
  case "$1" in --goos) GOOS="$2"; shift 2 ;; --goarch) GOARCH="$2"; shift 2 ;; *) echo "Unknown: $1"; exit 1 ;; esac
done

# Map Go os/arch to mise env vars
mise_env=""
if [[ -n "$GOOS" ]]; then
  os="$GOOS"; [[ "$os" == "darwin" ]] && os="macos"
  arch="$GOARCH"; [[ "$arch" == "amd64" ]] && arch="x86_64"; [[ "$arch" == "arm64" ]] && arch="aarch64"
  mise_env="MISE_OS=$os MISE_ARCH=$arch"

  # Clean previous platform's binaries (goreleaser calls per-target)
  find "$BINARIES_DIR" -name '*.gz' -delete 2>/dev/null || true
fi

download() {
  local name="$1" spec="$2"
  local dest="$BINARIES_DIR/${name}.gz"
  [[ -f "$dest" ]] && echo "EXISTS $name" && return

  local tmp; tmp="$(mktemp -d)"
  # Isolate mise data/cache per invocation to avoid race conditions
  # when goreleaser runs pre-hooks in parallel for different targets.
  local mise_data; mise_data="$(mktemp -d)"
  echo "DOWNLOAD $name ($spec) ${mise_env}"
  eval $mise_env MISE_DATA_DIR="$mise_data" MISE_CACHE_DIR="$mise_data/cache" mise install-into "$spec" "$tmp"

  local bin; bin="$(find "$tmp" -name "$name" -o -name "${name}.exe" | head -1)"
  [[ -z "$bin" ]] && { echo "WARN: $name not found"; rm -rf "$tmp" "$mise_data"; return; }

  gzip -9 -c "$bin" > "$dest"
  rm -rf "$tmp" "$mise_data"
  echo "OK $name ($(du -h "$dest" | cut -f1 | xargs))"
}

download fd  "github:sharkdp/fd@${FD_VERSION}"
download rg  "github:BurntSushi/ripgrep@${RG_VERSION}"
download rtk "github:rtk-ai/rtk@${RTK_VERSION}"

echo "Done."
