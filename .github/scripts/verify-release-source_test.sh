#!/usr/bin/env bash

set -euo pipefail

script=$(cd "$(dirname "$0")" && pwd)/verify-release-source.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

git init --quiet --bare "$tmp/origin.git"
git init --quiet "$tmp/repo"
git -C "$tmp/repo" config user.email release-test@stella.invalid
git -C "$tmp/repo" config user.name 'Stella Release Test'
git -C "$tmp/repo" remote add origin "$tmp/origin.git"

printf 'release\n' >"$tmp/repo/source"
git -C "$tmp/repo" add source
git -C "$tmp/repo" commit --quiet -m release
release_commit=$(git -C "$tmp/repo" rev-parse HEAD)
git -C "$tmp/repo" branch release/v0.63
git -C "$tmp/repo" push --quiet origin release/v0.63

test "$(cd "$tmp/repo" && "$script" v0.63.0 "$release_commit")" = release/v0.63
test "$(cd "$tmp/repo" && "$script" v0.63.1-rc.2 "$release_commit")" = release/v0.63

printf 'not released\n' >>"$tmp/repo/source"
git -C "$tmp/repo" commit --quiet -am unrelated
unreleased_commit=$(git -C "$tmp/repo" rev-parse HEAD)

if (cd "$tmp/repo" && "$script" v0.63.1 "$unreleased_commit") >/dev/null 2>&1; then
  echo 'accepted a commit outside release/v0.63' >&2
  exit 1
fi

if (cd "$tmp/repo" && "$script" v0.63 "$release_commit") >/dev/null 2>&1; then
  echo 'accepted an invalid release tag' >&2
  exit 1
fi
