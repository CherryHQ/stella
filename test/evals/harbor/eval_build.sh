#!/usr/bin/env bash
# Build eval-only binaries without making a fresh stellad rebuild regenerate.
set -euo pipefail

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

acquire_build_lock() {
  local lock=./dist/.eval-build.lock owner timeout=${STELLA_EVAL_BUILD_LOCK_TIMEOUT:-300}
  local deadline=$((SECONDS + timeout))
  mkdir -p ./dist
  while ! mkdir "$lock" 2>/dev/null; do
    owner=$(cat "$lock/pid" 2>/dev/null || true)
    [ "$SECONDS" -lt "$deadline" ] || {
      [ -n "$owner" ] || owner=unknown
      echo "eval:build: timed out waiting for $lock (owner PID: $owner); confirm that process is dead, then rm -rf $lock" >&2
      return 1
    }
    sleep 1
  done
  printf '%s\n' "$$" >"$lock/pid"
}

release_build_lock() { rm -rf ./dist/.eval-build.lock; }

main() {
  local identity stamp=./dist/bin/stellad.eval-source
  acquire_build_lock
  trap release_build_lock EXIT
  trap 'exit 129' HUP
  trap 'exit 130' INT
  trap 'exit 143' TERM
  identity=$(stellad_source_identity)

  if [ ! -f "$stamp" ] || [ "$(cat "$stamp")" != "$identity" ] ||
    tracked_sources_newer ./dist/bin/stellad cmd/stellad internal pkg plugins api resources web go.mod go.sum ||
    untracked_go_sources_newer ./dist/bin/stellad cmd/stellad internal pkg plugins; then
    local version commit build_date ldflags
    version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)
    commit=$(git rev-parse --short HEAD)
    build_date=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    ldflags="-X github.com/CherryHQ/stella/internal/platform/version.Version=$version -X github.com/CherryHQ/stella/internal/platform/version.Commit=$commit -X github.com/CherryHQ/stella/internal/platform/version.BuildDate=$build_date"
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
  if tracked_sources_newer ./dist/bin/testbed test/testbed internal/db internal/vault go.mod go.sum ||
    untracked_go_sources_newer ./dist/bin/testbed test/testbed internal/db internal/vault; then
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
