#!/usr/bin/env bash
set -euo pipefail

# Download fd, rg, mise, tap, rtk, and boxsh binaries, gzip-compressed into
# internal/embedded/binaries/.
#
# Uses `gh release download` with explicit platform-specific glob patterns.
# This avoids mise's cross-platform download bug where MISE_OS is ignored
# when the target arch matches the host arch (e.g. darwin/amd64 from Linux/amd64).
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
ISOLATED=false  # kept for CLI compatibility; no longer used

usage() {
  cat <<'EOF'
Usage:
  ./scripts/download-tools.sh [--goos <goos>] [--goarch <goarch>] [--isolated]

Options:
  --goos <goos>      Target GOOS. Defaults to `go env GOOS`.
  --goarch <goarch>  Target GOARCH. Defaults to `go env GOARCH`.
  --isolated         Ignored (kept for compatibility).
  -h, --help         Show this help.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --goos)     GOOS="$2";    shift 2 ;;
    --goarch)   GOARCH="$2";  shift 2 ;;
    --isolated) ISOLATED=true; shift  ;;
    -h|--help)  usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 1 ;;
  esac
done

target_goos="${GOOS:-$(go env GOOS)}"
target_goarch="${GOARCH:-$(go env GOARCH)}"
target_platform="${target_goos}-${target_goarch}"
target_dir="$BINARIES_DIR/$target_platform"
mkdir -p "$target_dir"

# Canonical architecture names used in release triples
case "$target_goarch" in
  amd64) TRIPLE_ARCH="x86_64" ;;
  arm64) TRIPLE_ARCH="aarch64" ;;
  *) echo "unsupported arch: $target_goarch" >&2; exit 1 ;;
esac

tmp_root="$(mktemp -d "${TMPDIR:-/tmp}/anna-tools.XXXXXX")"
trap 'rm -rf "$tmp_root"' EXIT

# Download a GitHub release asset, extract the named binary, and gzip it.
#
# $1 name        — binary name (also the output filename stem)
# $2 repo        — owner/repo
# $3 tag         — git tag
# $4 pattern     — glob passed to `gh release download --pattern`
# $5 optional    — "true" to skip gracefully on failure (default: false)
# $6 raw_binary  — "true" when the downloaded file IS the binary (no archive)
download() {
  local name="$1"
  local repo="$2"
  local tag="$3"
  local pattern="$4"
  local optional="${5:-false}"
  local raw_binary="${6:-false}"
  local dest="$target_dir/${name}.gz"

  if [[ -f "$dest" ]]; then
    echo "EXISTS $name ($target_platform)"
    return 0
  fi

  echo "DOWNLOAD $name ($target_platform) [$repo $tag $pattern]"

  local tmp_dir="$tmp_root/$name"
  mkdir -p "$tmp_dir"

  if ! gh release download "$tag" \
       --repo "$repo" \
       --pattern "$pattern" \
       --dir "$tmp_dir" 2>/dev/null; then
    if [[ "$optional" == "true" ]]; then
      echo "WARN: skipping optional $name (no asset: $pattern)"
      return 0
    fi
    echo "ERROR: gh release download failed: $repo $tag $pattern" >&2
    return 1
  fi

  local archive
  archive=$(find "$tmp_dir" -maxdepth 1 -type f | head -1)
  if [[ -z "$archive" ]]; then
    if [[ "$optional" == "true" ]]; then
      echo "WARN: skipping optional $name (no file downloaded)"
      return 0
    fi
    echo "ERROR: no file downloaded for $name" >&2
    return 1
  fi

  local bin
  if [[ "$raw_binary" == "true" ]]; then
    bin="$archive"
  else
    local extract_dir="$tmp_dir/x"
    mkdir -p "$extract_dir"
    if [[ "$archive" == *.zip ]]; then
      unzip -q "$archive" -d "$extract_dir"
    else
      tar -xzf "$archive" -C "$extract_dir"
    fi
    bin=$(find "$extract_dir" -type f \( -name "$name" -o -name "${name}.exe" \) -print -quit)
  fi

  if [[ -z "$bin" ]]; then
    if [[ "$optional" == "true" ]]; then
      echo "WARN: $name binary not found in archive"
      return 0
    fi
    echo "ERROR: $name binary not found in downloaded archive" >&2
    return 1
  fi

  local dest_tmp="$target_dir/.${name}.tmp"
  gzip -n -9 -c "$bin" > "$dest_tmp"
  mv "$dest_tmp" "$dest"
  echo "OK $name ($target_platform)"
}

# fd — sharkdp/fd
# Asset: fd-v{VERSION}-{triple}.{ext}
case "$target_goos" in
  darwin)  fd_triple="${TRIPLE_ARCH}-apple-darwin";       fd_ext="tar.gz" ;;
  linux)   fd_triple="${TRIPLE_ARCH}-unknown-linux-musl"; fd_ext="tar.gz" ;;
  windows) fd_triple="${TRIPLE_ARCH}-pc-windows-msvc";    fd_ext="zip" ;;
esac
download fd "sharkdp/fd" "v${FD_VERSION}" "fd-v*-${fd_triple}.${fd_ext}"

# rg — BurntSushi/ripgrep (binary inside archive is named "rg")
# Asset: ripgrep-{VERSION}-{triple}.{ext}
case "$target_goos" in
  darwin)  rg_triple="${TRIPLE_ARCH}-apple-darwin";        rg_ext="tar.gz" ;;
  linux)
    case "$target_goarch" in
      amd64) rg_triple="${TRIPLE_ARCH}-unknown-linux-musl" ;;
      arm64) rg_triple="${TRIPLE_ARCH}-unknown-linux-gnu" ;;
    esac
    rg_ext="tar.gz"
    ;;
  windows) rg_triple="${TRIPLE_ARCH}-pc-windows-msvc";     rg_ext="zip" ;;
esac
download rg "BurntSushi/ripgrep" "${RG_VERSION}" "ripgrep-*-${rg_triple}.${rg_ext}"

# mise — jdx/mise
# Asset: mise-{VERSION}-{os}-{arch}[-musl].{ext}
case "$target_goos" in
  darwin)  mise_os="macos";   mise_musl="";      mise_ext="tar.gz" ;;
  linux)   mise_os="linux";   mise_musl="-musl"; mise_ext="tar.gz" ;;
  windows) mise_os="windows"; mise_musl="";      mise_ext="zip" ;;
esac
case "$target_goarch" in
  amd64) mise_arch="x64" ;;
  arm64) mise_arch="arm64" ;;
esac
download mise "jdx/mise" "${MISE_VERSION}" "mise-*-${mise_os}-${mise_arch}${mise_musl}.${mise_ext}"

# tap — vaayne/tap
# Asset: tap_{VERSION}_{goos}_{goarch}.{ext}
case "$target_goos" in
  windows) tap_ext="zip" ;;
  *)       tap_ext="tar.gz" ;;
esac
download tap "vaayne/tap" "v${TAP_VERSION}" "tap_*_${target_goos}_${target_goarch}.${tap_ext}"

# rtk — rtk-ai/rtk (optional; no version in asset filename)
# Asset: rtk-{triple}.{ext}
case "$target_goos" in
  darwin)  rtk_triple="${TRIPLE_ARCH}-apple-darwin";       rtk_ext="tar.gz" ;;
  linux)
    case "$target_goarch" in
      amd64) rtk_triple="${TRIPLE_ARCH}-unknown-linux-musl" ;;
      arm64) rtk_triple="${TRIPLE_ARCH}-unknown-linux-gnu" ;;
    esac
    rtk_ext="tar.gz"
    ;;
  windows) rtk_triple="${TRIPLE_ARCH}-pc-windows-msvc";    rtk_ext="zip" ;;
esac
download rtk "rtk-ai/rtk" "v${RTK_VERSION}" "rtk-${rtk_triple}.${rtk_ext}" true

# boxsh — xicilion/boxsh (optional; raw binary, no archive, no Windows)
# Asset: boxsh-v{VERSION}-{os}-{arch}  (no file extension)
if [[ "$target_goos" != "windows" ]]; then
  case "$target_goos" in
    darwin) boxsh_os="darwin" ;;
    linux)  boxsh_os="linux" ;;
  esac
  case "$target_goarch" in
    amd64) boxsh_arch="x86_64" ;;
    arm64) boxsh_arch="arm64" ;;
  esac
  download boxsh "xicilion/boxsh" "v${BOXSH_VERSION}" \
    "boxsh-v*-${boxsh_os}-${boxsh_arch}" true true
fi

echo "Done."
