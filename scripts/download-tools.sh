#!/usr/bin/env bash
set -euo pipefail

# Download fd, rg, mise, tap, rtk, and boxsh binaries, gzip-compressed into
# internal/embedded/binaries/.
#
# Uses exact GitHub release asset URLs with curl instead of `gh release download`
# or `mise install`, which avoids CLI/tooling dependencies and platform-detection
# surprises in cross-platform release builds.
#
# Usage:
#   ./scripts/download-tools.sh
#   ./scripts/download-tools.sh --goos linux --goarch amd64
#   ./scripts/download-tools.sh --goos linux --goarch amd64 --isolated
#
# NOTE:
# fd v10.4.x no longer publishes a darwin/amd64 asset upstream, so darwin/amd64
# intentionally falls back to v10.3.0 for fd only.

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
BINARIES_DIR="$ROOT_DIR/internal/embedded/binaries"
mkdir -p "$BINARIES_DIR"

FD_VERSION="${FD_VERSION:-10.4.2}"
FD_DARWIN_AMD64_VERSION="${FD_DARWIN_AMD64_VERSION:-10.3.0}"
RG_VERSION="${RG_VERSION:-15.1.0}"
MISE_VERSION="${MISE_VERSION:-v2026.4.12}"
TAP_VERSION="${TAP_VERSION:-0.4.4}"
RTK_VERSION="${RTK_VERSION:-0.30.0}"
BOXSH_VERSION="${BOXSH_VERSION:-2.1.0}"

REPO_BASE_URL="https://github.com"
GITHUB_TOKEN="${GITHUB_TOKEN:-}"

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
  --isolated         Ignored (kept for compatibility).
  -h, --help         Show this help.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --goos)     GOOS="$2"; shift 2 ;;
    --goarch)   GOARCH="$2"; shift 2 ;;
    --isolated) ISOLATED=true; shift ;;
    -h|--help)  usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 1 ;;
  esac
done

target_goos="${GOOS:-$(go env GOOS)}"
target_goarch="${GOARCH:-$(go env GOARCH)}"
target_platform="${target_goos}-${target_goarch}"
target_dir="$BINARIES_DIR/$target_platform"
mkdir -p "$target_dir"

case "$target_goos" in
  darwin|linux|windows) ;;
  *) echo "unsupported os: $target_goos" >&2; exit 1 ;;
esac

case "$target_goarch" in
  amd64|arm64) ;;
  *) echo "unsupported arch: $target_goarch" >&2; exit 1 ;;
esac

tmp_root="$(mktemp -d "${TMPDIR:-/tmp}/anna-tools.XXXXXX")"
trap 'rm -rf "$tmp_root"' EXIT

curl_download() {
  local url="$1"
  local out="$2"

  if [[ -n "$GITHUB_TOKEN" ]]; then
    curl -fsSL --retry 3 --retry-delay 1 --connect-timeout 20 \
      -H "Authorization: Bearer $GITHUB_TOKEN" \
      -o "$out" \
      "$url"
    return
  fi

  curl -fsSL --retry 3 --retry-delay 1 --connect-timeout 20 \
    -o "$out" \
    "$url"
}

download() {
  local name="$1"
  local repo="$2"
  local tag="$3"
  local asset="$4"
  local optional="${5:-false}"
  local raw_binary="${6:-false}"
  local dest="$target_dir/${name}.gz"

  if [[ -f "$dest" ]]; then
    echo "EXISTS $name ($target_platform)"
    return 0
  fi

  local url="${REPO_BASE_URL}/${repo}/releases/download/${tag}/${asset}"
  echo "DOWNLOAD $name ($target_platform) [$repo $tag $asset]"

  local tmp_dir="$tmp_root/$name"
  mkdir -p "$tmp_dir"

  local archive="$tmp_dir/$asset"
  if ! curl_download "$url" "$archive"; then
    if [[ "$optional" == "true" ]]; then
      echo "WARN: skipping optional $name (no asset: $asset)"
      return 0
    fi
    echo "ERROR: curl download failed: $url" >&2
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

fd_asset() {
  case "$target_goos/$target_goarch" in
    darwin/amd64)
      printf 'v%s|fd-v%s-x86_64-apple-darwin.tar.gz\n' "$FD_DARWIN_AMD64_VERSION" "$FD_DARWIN_AMD64_VERSION"
      ;;
    darwin/arm64)
      printf 'v%s|fd-v%s-aarch64-apple-darwin.tar.gz\n' "$FD_VERSION" "$FD_VERSION"
      ;;
    linux/amd64)
      printf 'v%s|fd-v%s-x86_64-unknown-linux-musl.tar.gz\n' "$FD_VERSION" "$FD_VERSION"
      ;;
    linux/arm64)
      printf 'v%s|fd-v%s-aarch64-unknown-linux-musl.tar.gz\n' "$FD_VERSION" "$FD_VERSION"
      ;;
    windows/amd64)
      printf 'v%s|fd-v%s-x86_64-pc-windows-msvc.zip\n' "$FD_VERSION" "$FD_VERSION"
      ;;
    windows/arm64)
      printf 'v%s|fd-v%s-aarch64-pc-windows-msvc.zip\n' "$FD_VERSION" "$FD_VERSION"
      ;;
  esac
}

rg_asset() {
  case "$target_goos/$target_goarch" in
    darwin/amd64)  printf '15.1.0|ripgrep-15.1.0-x86_64-apple-darwin.tar.gz\n' ;;
    darwin/arm64)  printf '15.1.0|ripgrep-15.1.0-aarch64-apple-darwin.tar.gz\n' ;;
    linux/amd64)   printf '15.1.0|ripgrep-15.1.0-x86_64-unknown-linux-musl.tar.gz\n' ;;
    linux/arm64)   printf '15.1.0|ripgrep-15.1.0-aarch64-unknown-linux-gnu.tar.gz\n' ;;
    windows/amd64) printf '15.1.0|ripgrep-15.1.0-x86_64-pc-windows-msvc.zip\n' ;;
    windows/arm64) printf '15.1.0|ripgrep-15.1.0-aarch64-pc-windows-msvc.zip\n' ;;
  esac
}

mise_asset() {
  case "$target_goos/$target_goarch" in
    darwin/amd64)  printf '%s|mise-%s-macos-x64.tar.gz\n' "$MISE_VERSION" "$MISE_VERSION" ;;
    darwin/arm64)  printf '%s|mise-%s-macos-arm64.tar.gz\n' "$MISE_VERSION" "$MISE_VERSION" ;;
    linux/amd64)   printf '%s|mise-%s-linux-x64-musl.tar.gz\n' "$MISE_VERSION" "$MISE_VERSION" ;;
    linux/arm64)   printf '%s|mise-%s-linux-arm64-musl.tar.gz\n' "$MISE_VERSION" "$MISE_VERSION" ;;
    windows/amd64) printf '%s|mise-%s-windows-x64.zip\n' "$MISE_VERSION" "$MISE_VERSION" ;;
    windows/arm64) printf '%s|mise-%s-windows-arm64.zip\n' "$MISE_VERSION" "$MISE_VERSION" ;;
  esac
}

tap_asset() {
  case "$target_goos/$target_goarch" in
    darwin/amd64)  printf 'v%s|tap_%s_darwin_amd64.tar.gz\n' "$TAP_VERSION" "$TAP_VERSION" ;;
    darwin/arm64)  printf 'v%s|tap_%s_darwin_arm64.tar.gz\n' "$TAP_VERSION" "$TAP_VERSION" ;;
    linux/amd64)   printf 'v%s|tap_%s_linux_amd64.tar.gz\n' "$TAP_VERSION" "$TAP_VERSION" ;;
    linux/arm64)   printf 'v%s|tap_%s_linux_arm64.tar.gz\n' "$TAP_VERSION" "$TAP_VERSION" ;;
    windows/amd64) printf 'v%s|tap_%s_windows_amd64.zip\n' "$TAP_VERSION" "$TAP_VERSION" ;;
    windows/arm64) printf 'v%s|tap_%s_windows_arm64.zip\n' "$TAP_VERSION" "$TAP_VERSION" ;;
  esac
}

rtk_asset() {
  case "$target_goos/$target_goarch" in
    darwin/amd64)  printf 'v%s|rtk-x86_64-apple-darwin.tar.gz\n' "$RTK_VERSION" ;;
    darwin/arm64)  printf 'v%s|rtk-aarch64-apple-darwin.tar.gz\n' "$RTK_VERSION" ;;
    linux/amd64)   printf 'v%s|rtk-x86_64-unknown-linux-musl.tar.gz\n' "$RTK_VERSION" ;;
    linux/arm64)   printf 'v%s|rtk-aarch64-unknown-linux-gnu.tar.gz\n' "$RTK_VERSION" ;;
    windows/amd64) printf 'v%s|rtk-x86_64-pc-windows-msvc.zip\n' "$RTK_VERSION" ;;
    windows/arm64) return 1 ;;
  esac
}

boxsh_asset() {
  case "$target_goos/$target_goarch" in
    darwin/amd64) printf 'v%s|boxsh-v%s-darwin-x86_64\n' "$BOXSH_VERSION" "$BOXSH_VERSION" ;;
    darwin/arm64) printf 'v%s|boxsh-v%s-darwin-arm64\n' "$BOXSH_VERSION" "$BOXSH_VERSION" ;;
    linux/amd64)  printf 'v%s|boxsh-v%s-linux-x64\n' "$BOXSH_VERSION" "$BOXSH_VERSION" ;;
    linux/arm64)  printf 'v%s|boxsh-v%s-linux-arm64\n' "$BOXSH_VERSION" "$BOXSH_VERSION" ;;
    windows/*) return 1 ;;
  esac
}

IFS='|' read -r fd_tag fd_file <<<"$(fd_asset)"
download fd "sharkdp/fd" "$fd_tag" "$fd_file"

IFS='|' read -r rg_tag rg_file <<<"$(rg_asset)"
download rg "BurntSushi/ripgrep" "$rg_tag" "$rg_file"

IFS='|' read -r mise_tag mise_file <<<"$(mise_asset)"
download mise "jdx/mise" "$mise_tag" "$mise_file"

IFS='|' read -r tap_tag tap_file <<<"$(tap_asset)"
download tap "vaayne/tap" "$tap_tag" "$tap_file"

if rtk_meta="$(rtk_asset)"; then
  IFS='|' read -r rtk_tag rtk_file <<<"$rtk_meta"
  download rtk "rtk-ai/rtk" "$rtk_tag" "$rtk_file" true
fi

if boxsh_meta="$(boxsh_asset)"; then
  IFS='|' read -r boxsh_tag boxsh_file <<<"$boxsh_meta"
  download boxsh "xicilion/boxsh" "$boxsh_tag" "$boxsh_file" true true
fi

echo "Done."
