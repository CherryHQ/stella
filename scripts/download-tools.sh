#!/usr/bin/env bash
set -euo pipefail

# Download fd, rg, mise, tap, rtk, and boxsh binaries, gzip-compressed into
# internal/embedded/binaries/.
#
# Default mode is tuned for local/dev use:
# - reuse the normal mise cache/store
# - avoid `mise install-into`, which has been fragile and process-heavy here
#
# Use --isolated for CI/release-style runs when you want an isolated mise
# data/cache directory per tool.
#
# Usage:
#   ./scripts/download-tools.sh
#   ./scripts/download-tools.sh --goos linux --goarch amd64
#   ./scripts/download-tools.sh --goos linux --goarch amd64 --isolated

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
BINARIES_DIR="$ROOT_DIR/internal/embedded/binaries"
mkdir -p "$BINARIES_DIR"

FD_VERSION="${FD_VERSION:-10.4.2}"
RG_VERSION="${RG_VERSION:-15.1.0}"
MISE_VERSION="${MISE_VERSION:-v2026.4.12}"
TAP_VERSION="${TAP_VERSION:-0.4.4}"
RTK_VERSION="${RTK_VERSION:-0.30.0}"
BOXSH_VERSION="${BOXSH_VERSION:-2.1.0}"

GOOS=""
GOARCH=""
ISOLATED=false

usage() {
  cat <<'EOF'
Usage:
  ./scripts/download-tools.sh [--goos <goos>] [--goarch <goarch>] [--isolated]

Options:
  --goos <goos>      Target GOOS. Defaults to `go env GOOS`.
  --goarch <goarch>  Target GOARCH. Defaults to `go env GOARCH`.
  --isolated         Use isolated mise data/cache per tool. Useful for CI/release.
  -h, --help         Show this help.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --goos)
      GOOS="$2"
      shift 2
      ;;
    --goarch)
      GOARCH="$2"
      shift 2
      ;;
    --isolated)
      ISOLATED=true
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown argument: $1" >&2
      usage >&2
      exit 1
      ;;
  esac
done

target_goos="${GOOS:-$(go env GOOS)}"
target_goarch="${GOARCH:-$(go env GOARCH)}"
target_platform="${target_goos}-${target_goarch}"
target_dir="$BINARIES_DIR/$target_platform"
mkdir -p "$target_dir"

os="$target_goos"
[[ "$os" == "darwin" ]] && os="macos"

arch="$target_goarch"
[[ "$arch" == "amd64" ]] && arch="x86_64"
[[ "$arch" == "arm64" ]] && arch="aarch64"

tmp_root="$(mktemp -d "${TMPDIR:-/tmp}/anna-tools.XXXXXX")"
cleanup() {
  rm -rf "$tmp_root"
}
trap cleanup EXIT

maybe_trust_project() {
  command mise trust "$ROOT_DIR" >/dev/null 2>&1 || true
}

run_mise() {
  local tool_name="$1"
  shift

  if [[ "$ISOLATED" == "true" ]]; then
    local mise_data="$tmp_root/mise-$tool_name"
    mkdir -p "$mise_data/cache"
    env \
      MISE_OS="$os" \
      MISE_ARCH="$arch" \
      MISE_YES=1 \
      MISE_DATA_DIR="$mise_data" \
      MISE_CACHE_DIR="$mise_data/cache" \
      mise "$@"
    return
  fi

  env \
    MISE_OS="$os" \
    MISE_ARCH="$arch" \
    MISE_YES=1 \
    mise "$@"
}

find_binary() {
  local dir="$1"
  local name="$2"
  local candidate

  for candidate in \
    "$dir/$name" \
    "$dir/${name}.exe" \
    "$dir/bin/$name" \
    "$dir/bin/${name}.exe"
  do
    if [[ -f "$candidate" ]]; then
      printf '%s\n' "$candidate"
      return 0
    fi
  done

  find "$dir" -type f \( -name "$name" -o -name "${name}.exe" \) -print -quit
}

download() {
  local name="$1"
  local spec="$2"
  local optional="${3:-false}"
  local dest="$target_dir/${name}.gz"

  if [[ -f "$dest" ]]; then
    echo "EXISTS $name ($target_platform)"
    return
  fi

  echo "DOWNLOAD $name ($spec) MISE_OS=$os MISE_ARCH=$arch"

  if ! run_mise "$name" install "$spec"; then
    if [[ "$optional" == "true" ]]; then
      echo "WARN: skipping optional tool $name for $target_platform"
      return
    fi
    return 1
  fi

  local install_dir
  if ! install_dir="$(run_mise "$name" where "$spec")"; then
    if [[ "$optional" == "true" ]]; then
      echo "WARN: install path unavailable for optional tool $name"
      return
    fi
    return 1
  fi

  local bin
  bin="$(find_binary "$install_dir" "$name")"
  if [[ -z "$bin" ]]; then
    if [[ "$optional" == "true" ]]; then
      echo "WARN: $name binary not found under $install_dir"
      return
    fi
    echo "ERROR: $name binary not found under $install_dir" >&2
    return 1
  fi

  local dest_tmp="$target_dir/.${name}.tmp"
  gzip -n -9 -c "$bin" > "$dest_tmp"
  mv "$dest_tmp" "$dest"
  echo "OK $name ($target_platform)"
}

maybe_trust_project

download fd    "github:sharkdp/fd@${FD_VERSION}"
download rg    "github:BurntSushi/ripgrep@${RG_VERSION}"
download mise  "github:jdx/mise@${MISE_VERSION}"
download tap   "github:vaayne/tap@${TAP_VERSION}"
download rtk   "github:rtk-ai/rtk@${RTK_VERSION}" true
download boxsh "github:xicilion/boxsh@${BOXSH_VERSION}" true

echo "Done."
