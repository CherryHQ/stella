#!/usr/bin/env bash
# Resolve GITHUB_TOKEN for image builds that fetch release assets.
#
# Sourced, not executed: it exports into the caller's shell. Lives outside
# .mise/tasks/ so mise cannot mistake it for a task.
#
# Order: an explicit GITHUB_TOKEN, then GH_TOKEN, then whatever `gh` is logged
# in as. A missing token is not an error; the image builds without the secret
# and the caller decides what that means.
export GITHUB_TOKEN="${GITHUB_TOKEN:-${GH_TOKEN:-}}"
if [ -z "$GITHUB_TOKEN" ] && command -v gh >/dev/null 2>&1; then
  GITHUB_TOKEN="$(gh auth token 2>/dev/null || true)"
  export GITHUB_TOKEN
fi
