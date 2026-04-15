#!/usr/bin/env bash
set -eo pipefail

# Download fd, rg, rtk, and boxsh binaries, gzip-compressed into
# internal/embedded/binaries/.
#
# Usage:
#   ./scripts/download-tools.sh                                # current platform (dev)
#   ./scripts/download-tools.sh --goos linux --goarch amd64    # cross-platform (release)

BINARIES_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/internal/embedded/binaries"
mkdir -p "$BINARIES_DIR"

FD_VERSION="${FD_VERSION:-10.4.2}"
RG_VERSION="${RG_VERSION:-15.1.0}"
RTK_VERSION="${RTK_VERSION:-0.30.0}"
BOXSH_VERSION="${BOXSH_VERSION:-2.1.0}"

GOOS="" GOARCH=""
while [[ $# -gt 0 ]]; do
  case "$1" in --goos) GOOS="$2"; shift 2 ;; --goarch) GOARCH="$2"; shift 2 ;; *) echo "Unknown: $1"; exit 1 ;; esac
done

target_goos="${GOOS:-$(go env GOOS)}"
target_goarch="${GOARCH:-$(go env GOARCH)}"
target_platform="${target_goos}-${target_goarch}"
target_dir="$BINARIES_DIR/$target_platform"
mkdir -p "$target_dir"

# Map Go os/arch to mise env vars
mise_env=""
os="$target_goos"; [[ "$os" == "darwin" ]] && os="macos"
arch="$target_goarch"; [[ "$arch" == "amd64" ]] && arch="x86_64"; [[ "$arch" == "arm64" ]] && arch="aarch64"
mise_env="MISE_OS=$os MISE_ARCH=$arch"

download() {
  local name="$1" spec="$2" optional="${3:-false}"
  local dest="$target_dir/${name}.gz"
  [[ -f "$dest" ]] && echo "EXISTS $name ($target_platform)" && return

  local tmp; tmp="$(mktemp -d)"
  # Isolate mise data/cache per invocation to avoid race conditions
  # when goreleaser runs pre-hooks in parallel for different targets.
  local mise_data; mise_data="$(mktemp -d)"
  echo "DOWNLOAD $name ($spec) ${mise_env}"
  if ! eval $mise_env MISE_DATA_DIR="$mise_data" MISE_CACHE_DIR="$mise_data/cache" mise install-into "$spec" "$tmp"; then
    rm -rf "$tmp" "$mise_data"
    if [[ "$optional" == "true" ]]; then
      echo "WARN: skipping optional tool $name for $target_platform"
      return
    fi
    return 1
  fi

  local bin; bin="$(find "$tmp" -name "$name" -o -name "${name}.exe" | head -1)"
  [[ -z "$bin" ]] && { echo "WARN: $name not found"; rm -rf "$tmp" "$mise_data"; return; }

  local dest_tmp; dest_tmp="$(mktemp "$target_dir/.${name}.XXXXXX")"
  gzip -n -9 -c "$bin" > "$dest_tmp"
  mv "$dest_tmp" "$dest"
  rm -rf "$tmp" "$mise_data"
  echo "OK $name ($target_platform, $(du -h "$dest" | cut -f1 | xargs))"
}

download fd    "github:sharkdp/fd@${FD_VERSION}"
download rg    "github:BurntSushi/ripgrep@${RG_VERSION}"
download rtk   "github:rtk-ai/rtk@${RTK_VERSION}" true
download boxsh "github:xicilion/boxsh@${BOXSH_VERSION}" true

echo "Done."
