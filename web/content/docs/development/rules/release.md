---
title: Release
description: Release tagging and packaging workflow for Stella.
---

## Tag Format

Use semantic versioning with `v` prefix: `v0.1.0`, `v1.0.0`, `v1.2.3-rc.1`.
GoReleaser auto-detects pre-release suffixes (`-rc.1`, `-beta.1`).

## Branch Model

`main` remains the default development branch. Releases use maintained minor
branches named `release/vX.Y`, for example `release/v0.63`:

- Cut a new minor release branch from the intended commit on `main`.
- Publish `vX.Y.0` and every `vX.Y.Z` patch from that same branch.
- Land fixes on `main` first when practical, then backport the exact commit to a
  short-lived branch and open a PR against `release/vX.Y`.
- Sync release metadata and any release-only fix back to `main` through a PR.
  Never leave the two lines silently divergent.

Protect `release/v*` with the same required Format and Test checks as `main`.
Disallow force-pushes and branch deletion, and require PRs for updates. The tag
workflow also derives `release/vX.Y` from the version and rejects a tagged
commit that is not reachable from that branch.

## Release Flow

1. Choose release tag `vX.Y.Z`; the web package version is `X.Y.Z` (without `v`).
2. Create or update the maintained release branch:

   ```bash
   RELEASE_BRANCH=release/vX.Y
   git fetch origin --prune

   # New minor line only:
   git switch main
   git pull --ff-only origin main
   git switch -c "$RELEASE_BRANCH"
   git push -u origin "$RELEASE_BRANCH"

   # Existing patch line:
   git switch "$RELEASE_BRANCH"
   git pull --ff-only origin "$RELEASE_BRANCH"
   ```

3. Create a short-lived preparation branch from `release/vX.Y`. Do not commit
   release metadata directly to the maintained branch.
4. Update `web/package.json` so `.version` matches the API/server release version:
   ```bash
   VERSION=X.Y.Z
   tmp=$(mktemp)
   jq --arg version "$VERSION" '.version = $version' web/package.json > "$tmp" && mv "$tmp" web/package.json
   test "$(jq -r '.version' web/package.json)" = "$VERSION"
   ```
5. Update the Helm chart metadata in `deploy/helm/stella/Chart.yaml`:
   - Set `appVersion: "vX.Y.Z"` so the chart records the release it ships alongside.
     The default `image.tag` is `latest` (CI publishes it for every stable release,
     see Artifacts), so a fresh install already tracks this version; `appVersion`
     just keeps the metadata honest.
   - Bump the chart's own `version` (its SemVer, independent of `appVersion`)
     whenever the chart changed since the last release.
6. Update `web/content/docs/changelog.mdx` and `web/content/docs/changelog.zh.mdx` (see below).
7. Commit: `📝 docs: Update CHANGELOG for vX.Y.Z` including both changelogs,
   `web/package.json`, and `deploy/helm/stella/Chart.yaml`.
8. Run the full pre-cut gate below. Verify that the release commit is `HEAD` and
   the working tree is clean:
   ```bash
   git status --short
   git log --oneline -1
   ```
9. Verify that the version milestone contains the exact release scope and has
   no open issues. Stop and resolve any open issue before tagging:
   ```bash
   gh issue list --repo CherryHQ/stella --milestone vX.Y.Z --state open
   gh issue list --repo CherryHQ/stella --milestone vX.Y.Z --state all
   ```
10. Push the preparation branch and open a PR against `release/vX.Y`. Wait for
    required checks and merge it. GitHub auto-closes linked issues only when a PR
    merges into the default branch, so close the release issue explicitly and
    recheck that the version milestone has no open scope.
11. Fetch the merged release branch, then tag its remote tip:
    ```bash
    git fetch origin --prune
    git switch "$RELEASE_BRANCH"
    git reset --hard "origin/$RELEASE_BRANCH"
    git tag vX.Y.Z
    test "$(git rev-parse vX.Y.Z)" = "$(git rev-parse origin/$RELEASE_BRANCH)"
    git push origin vX.Y.Z
    ```
12. CI triggers `.github/workflows/release.yml`. It verifies the exact tagged
    commit and the matching maintained branch before publication starts.
13. After CI succeeds and the GitHub Release is visible, open a sync-back PR
    from `release/vX.Y` to `main` when the release branch contains commits not
    already present on `main`.
14. Close the version milestone:
    ```bash
    MILESTONE_NUMBER=$(gh api 'repos/CherryHQ/stella/milestones?state=open' \
      --jq '.[] | select(.title == "vX.Y.Z") | .number')
    test -n "$MILESTONE_NUMBER"
    gh api --method PATCH "repos/CherryHQ/stella/milestones/$MILESTONE_NUMBER" \
      -f state=closed
    ```

The version milestone is the durable release record. Do not create a duplicate
`release:vX.Y.Z` label.

## Update Changelog

The changelogs live at `web/content/docs/changelog.mdx` and `web/content/docs/changelog.zh.mdx` (rendered on the docs site).
They have YAML frontmatter — preserve it when editing. Only modify content below the `---` block, and keep English and Chinese entries in sync.

Gather changes since last tag:

```bash
git log $(git describe --tags --abbrev=0 2>/dev/null || git rev-list --max-parents=0 HEAD)..HEAD --oneline
gh pr list --state merged --base "$RELEASE_BRANCH" --search "merged:>=$(git log -1 --format=%aI $(git describe --tags --abbrev=0 2>/dev/null || git rev-list --max-parents=0 HEAD))"
```

Apply to both changelog files:

1. Rename `[Unreleased]` to `[X.Y.Z] - YYYY-MM-DD`
2. Add fresh `[Unreleased]` section above
3. Categorize: `✨ Features`, `🐛 Bug Fixes`, `♻️ Refactoring`, `📝 Documentation`, `📦 Dependencies`
4. Link PRs: `([#123](https://github.com/CherryHQ/stella/pull/123))`
5. Append: `**Full Changelog**: [vPREV...vX.Y.Z](https://github.com/CherryHQ/stella/compare/vPREV...vX.Y.Z)`

## Validate and Test

Run the full pre-cut gate — it executes, strictly in order, `format` → `build` →
`test` → `system-test` → `release:check` → `release:snapshot`:

```bash
VERSION=X.Y.Z
test "$(jq -r '.version' web/package.json)" = "$VERSION"
mise run release:validate
```

The local gate remains sequential so the first failure stops later work. The
tag workflow verifies the release source once, then runs quality/build, Go and
Web tests, the System Test, and the release snapshot in parallel. GoReleaser and
Docker publication depend on all four jobs. A failure in any lane blocks every
publication job.

Release CI pins supported Ubuntu runners. The System Test lane uploads its server
logs with `if: always()` before any other job can affect its isolated workspace,
so failed subprocess journeys remain diagnosable. See `system-test.md`.

## Artifacts

- **Binaries**: linux/darwin/windows × amd64/arm64 (GoReleaser)
- **Docker**: `ghcr.io/cherryhq/stella` — linux/amd64 + linux/arm64
- **Docker tags**: `latest` (stable), `vX.Y.Z` (release), SHA (every build)
