#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."

# Exercise the actual required-check command for all GitHub job outcomes. A
# skipped/cancelled prerequisite must never turn the aggregate green.
pr=.github/workflows/lint-and-test.yml
release=.github/workflows/release.yml
sandbox=.github/workflows/sandbox-docker.yml
test "$(yq '.jobs.test.if' "$pr")" = 'always()'
test "$(yq '.jobs.test.needs | sort | join(",")' "$pr")" = 'packages,system'
join_command=$(yq '.jobs.test.steps[0].run' "$pr")
for packages in success failure cancelled skipped; do
  for system in success failure cancelled skipped; do
    if PACKAGE_RESULT=$packages SYSTEM_RESULT=$system bash -c "$join_command"; then
      test "$packages/$system" = success/success
    else
      test "$packages/$system" != success/success
    fi
  done
done

# All validation and image candidates must succeed before GoReleaser can publish.
test "$(yq '.jobs.goreleaser.needs | sort | join(",")' "$release")" = 'docker,quality,sandbox,system,tests'
test "$(yq '.jobs.merge.needs | sort | join(",")' "$release")" = 'docker,goreleaser,sandbox'
test "$(yq '.jobs.source.steps[] | select(.name == "Verify tag identity and release branch") | .if' "$release")" = "github.event_name == 'push'"
test "$(yq '.on.push | keys | join(",")' "$release")" = tags
test "$(yq '.jobs.goreleaser.steps[] | select(.name == "Run GoReleaser") | .if' "$release")" = "github.event_name == 'push'"
test "$(yq '.jobs.merge.if' "$release")" = "github.event_name == 'push'"
test "$(yq '.on.push.tags' "$sandbox")" = null
test "$(yq '.jobs.sandbox.with.candidate' "$release")" = true

# Read-only rehearsals must not push candidate images, even on workflow_dispatch.
push_condition="\${{ github.event_name == 'push' }}"
test "$(yq '.jobs.docker.steps[] | select(.id == "build") | .with.push' "$release")" = "$push_condition"
test "$(yq '.jobs.build.steps[] | select(.id == "build") | .with.push' "$sandbox")" = "$push_condition"

echo 'CI policy: aggregate outcomes and release publication gates passed'
