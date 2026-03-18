#!/usr/bin/env bash
set -eo pipefail

# Download fd, rg, rtk for the current platform using mise.
# Binaries are gzip-compressed into internal/embedded/binaries/ for go:embed.

BINARIES_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/internal/embedded/binaries"
mkdir -p "$BINARIES_DIR"

FD_VERSION="${FD_VERSION:-10.4.2}"
RG_VERSION="${RG_VERSION:-15.1.0}"
RTK_VERSION="${RTK_VERSION:-0.30.0}"

download() {
  local name="$1" spec="$2"
  local dest="$BINARIES_DIR/${name}.gz"
  if [[ -f "$dest" ]]; then echo "EXISTS $name"; return; fi

  local tmp; tmp="$(mktemp -d)"
  echo "DOWNLOAD $name ($spec)"
  mise install-into "$spec" "$tmp"

  local bin; bin="$(find "$tmp" -name "$name" -o -name "${name}.exe" | head -1)"
  if [[ -z "$bin" ]]; then echo "WARN: $name not found"; rm -rf "$tmp"; return; fi

  gzip -9 -c "$bin" > "$dest"
  rm -rf "$tmp"
  echo "OK $name ($(du -h "$dest" | cut -f1 | xargs))"
}

download fd  "github:sharkdp/fd@${FD_VERSION}"
download rg  "github:BurntSushi/ripgrep@${RG_VERSION}"
download rtk "github:rtk-ai/rtk@${RTK_VERSION}"

echo "Done."
