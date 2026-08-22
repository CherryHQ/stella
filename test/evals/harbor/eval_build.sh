#!/usr/bin/env bash
# Build eval-only binaries without making a fresh stellad rebuild regenerate.
set -euo pipefail

HARBOR_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=stellad_wrapper.sh
source "$HARBOR_DIR/stellad_wrapper.sh"

tracked_sources_newer() {
  local binary=$1 source
  shift
  [ -x "$binary" ] || return 0
  while IFS= read -r source; do
    [ "$source" -nt "$binary" ] && return 0
  done < <(git ls-files -- "$@")
  return 1
}

untracked_go_sources_newer() {
  local binary=$1 source
  shift
  [ -x "$binary" ] || return 0
  while IFS= read -r source; do
    git ls-files --error-unmatch "$source" >/dev/null 2>&1 && continue
    [ "$source" -nt "$binary" ] && return 0
  done < <(find "$@" -type f -name '*.go' -print 2>/dev/null)
  return 1
}

stellad_source_identity() {
  {
    git rev-parse HEAD
    git status --porcelain -- cmd/stellad internal pkg plugins api resources web go.mod go.sum
  } | shasum -a 256 | awk '{print $1}'
}

main() {
  local identity stamp=./dist/bin/stellad.eval-source
  recover_stale_stellad_binary ./dist/bin/stellad
  identity=$(stellad_source_identity)

  if [ ! -f "$stamp" ] || [ "$(cat "$stamp")" != "$identity" ] ||
    tracked_sources_newer ./dist/bin/stellad cmd/stellad internal pkg plugins api resources web go.mod go.sum ||
    untracked_go_sources_newer ./dist/bin/stellad cmd/stellad internal pkg plugins; then
    local version commit build_date ldflags
    version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)
    commit=$(git rev-parse --short HEAD)
    build_date=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    ldflags="-X github.com/CherryHQ/stella/internal/version.Version=$version -X github.com/CherryHQ/stella/internal/version.Commit=$commit -X github.com/CherryHQ/stella/internal/version.BuildDate=$build_date"
    mise run generate
    rm -rf ./dist/bin
    mkdir -p ./dist/bin
    go build -ldflags="$ldflags" -o ./dist/bin/stellad ./cmd/stellad
    identity=$(stellad_source_identity)
    printf '%s\n' "$identity" >"$stamp"
  else
    echo "eval:build: reusing dist/bin/stellad (newer than sources, source identity unchanged)"
  fi

  mkdir -p ./dist/bin ./dist/bin-eval
  if tracked_sources_newer ./dist/bin/testbed test/testbed internal/db internal/pgruntime internal/vault go.mod go.sum ||
    untracked_go_sources_newer ./dist/bin/testbed test/testbed internal/db internal/pgruntime internal/vault; then
    go build -o ./dist/bin/testbed ./test/testbed
  fi
  if tracked_sources_newer ./dist/bin-eval/stella-eval-agent cmd/stella-eval-agent internal pkg plugins test/evals/harbor go.mod go.sum ||
    untracked_go_sources_newer ./dist/bin-eval/stella-eval-agent cmd/stella-eval-agent internal pkg plugins test/evals/harbor; then
    go build -o ./dist/bin-eval/stella-eval-agent ./cmd/stella-eval-agent
  fi
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  main "$@"
fi
