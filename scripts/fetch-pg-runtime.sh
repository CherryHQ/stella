#!/usr/bin/env bash
set -euo pipefail

REPO="${STELLA_PG_RUNTIME_REPO:-CherryHQ/stella-pg-runtime}"
VERSION="${STELLA_PG_RUNTIME_VERSION:-pg18.4-pgvector0.8.2-pgsearch0.24.1}"
GOOS_VALUE="${GOOS:-$(go env GOOS)}"
GOARCH_VALUE="${GOARCH:-$(go env GOARCH)}"
SOURCE="${STELLA_PG_RUNTIME_SOURCE:-}"

linux_source() {
  local codename=""
  if [[ -r /etc/os-release ]]; then
    codename="$(. /etc/os-release && printf '%s' "${VERSION_CODENAME:-${UBUNTU_CODENAME:-}}")"
  fi
  case "$codename" in
    bookworm|jammy|noble|resolute|trixie) printf '%s' "$codename" ;;
    *) return 1 ;;
  esac
}

if [[ -z "$SOURCE" ]]; then
  case "$GOOS_VALUE-$GOARCH_VALUE" in
    darwin-arm64) SOURCE="postgresapp" ;;
    linux-amd64|linux-arm64)
      if ! SOURCE="$(linux_source)"; then
        echo "no default PostgreSQL runtime source for $GOOS_VALUE-$GOARCH_VALUE on this Linux distro" >&2
        echo "set STELLA_PG_RUNTIME_SOURCE to one of: bookworm, jammy, noble, resolute, trixie" >&2
        exit 1
      fi
      ;;
    *)
      echo "no default PostgreSQL runtime source for $GOOS_VALUE-$GOARCH_VALUE" >&2
      echo "set STELLA_PG_RUNTIME_SOURCE after a bundle exists in $REPO" >&2
      exit 1
      ;;
  esac
fi

ASSET="stella-pg-runtime-$VERSION-$GOOS_VALUE-$GOARCH_VALUE-$SOURCE.tar.zst"
TAG="$VERSION"
DEST_DIR="resources/pgbundle/bundles/$GOOS_VALUE-$GOARCH_VALUE"
if [[ "$GOOS_VALUE" == "linux" ]]; then
  DEST_DIR="$DEST_DIR/$SOURCE"
fi
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

BASE_URL="https://github.com/$REPO/releases/download/$TAG"
curl -fsSLo "$TMP_DIR/$ASSET" "$BASE_URL/$ASSET"
curl -fsSLo "$TMP_DIR/$ASSET.sha256" "$BASE_URL/$ASSET.sha256"

expected_sha="$(awk '{print $1}' "$TMP_DIR/$ASSET.sha256")"
if command -v sha256sum >/dev/null 2>&1; then
  actual_sha="$(sha256sum "$TMP_DIR/$ASSET" | awk '{print $1}')"
else
  actual_sha="$(shasum -a 256 "$TMP_DIR/$ASSET" | awk '{print $1}')"
fi
if [[ "$actual_sha" != "$expected_sha" ]]; then
  echo "checksum mismatch for $ASSET" >&2
  echo "expected $expected_sha" >&2
  echo "actual   $actual_sha" >&2
  exit 1
fi

mkdir -p "$DEST_DIR"
cp "$TMP_DIR/$ASSET" "$DEST_DIR/pg-runtime.tar.zst"
printf '%s\n' "$expected_sha" > "$DEST_DIR/pg-runtime.sha256"

echo "wrote $DEST_DIR/pg-runtime.tar.zst"
