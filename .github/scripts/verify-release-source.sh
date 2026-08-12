#!/usr/bin/env bash

set -euo pipefail

tag=${1:?usage: verify-release-source.sh <tag> [commit] [remote]}
commit=${2:-HEAD}
remote=${3:-origin}

if [[ ! "$tag" =~ ^v([0-9]+)\.([0-9]+)\.([0-9]+)(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$ ]]; then
  echo "invalid release tag: $tag" >&2
  exit 1
fi

release_branch="release/v${BASH_REMATCH[1]}.${BASH_REMATCH[2]}"
commit_sha=$(git rev-parse "${commit}^{commit}")
remote_ref="refs/remotes/${remote}/${release_branch}"

git fetch --quiet --no-tags "$remote" \
  "refs/heads/${release_branch}:${remote_ref}"

if ! git merge-base --is-ancestor "$commit_sha" "$remote_ref"; then
  echo "release commit $commit_sha is not on $release_branch" >&2
  exit 1
fi

printf '%s\n' "$release_branch"
