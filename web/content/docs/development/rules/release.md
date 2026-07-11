---
title: Release
description: Release tagging and packaging workflow for Stella.
---

## Tag Format

Use semantic versioning with `v` prefix: `v0.1.0`, `v1.0.0`, `v1.2.3-rc.1`.
GoReleaser auto-detects pre-release suffixes (`-rc.1`, `-beta.1`).

## Release Flow

1. Choose release tag `vX.Y.Z`; the web package version is `X.Y.Z` (without `v`).
2. Update `web/package.json` so `.version` matches the API/server release version:
   ```bash
   VERSION=X.Y.Z
   tmp=$(mktemp)
   jq --arg version "$VERSION" '.version = $version' web/package.json > "$tmp" && mv "$tmp" web/package.json
   test "$(jq -r '.version' web/package.json)" = "$VERSION"
   ```
3. Update the Helm chart metadata in `deploy/helm/stella/Chart.yaml`:
   - Set `appVersion: "vX.Y.Z"` so the chart records the release it ships alongside.
     The default `image.tag` is `latest` (CI publishes it for every stable release,
     see Artifacts), so a fresh install already tracks this version; `appVersion`
     just keeps the metadata honest.
   - Bump the chart's own `version` (its SemVer, independent of `appVersion`)
     whenever the chart changed since the last release.
4. Update `web/content/docs/changelog.mdx` and `web/content/docs/changelog.zh.mdx` (see below).
5. Commit: `📝 docs: Update CHANGELOG for vX.Y.Z` including both changelogs, `web/package.json`, and `deploy/helm/stella/Chart.yaml`.
6. Verify the commit succeeded and the working tree is clean:
   ```bash
   git status --short
   git log --oneline -1
   ```
   Stop if the release commit is not `HEAD`; never tag before the commit exists.
7. Tag the release commit and verify the tag points at `HEAD`:
   ```bash
   git tag vX.Y.Z
   test "$(git rev-parse vX.Y.Z)" = "$(git rev-parse HEAD)"
   ```
8. Tag release issues with the release label so they are traceable on the GitHub Project board:

   ```bash
   # warn about any issue still open in the milestone before tagging
   gh issue list --repo CherryHQ/stella --milestone vX.Y.Z --state open

   # label every issue in the milestone with release:vX.Y.Z (--add-label is idempotent)
   gh issue list --repo CherryHQ/stella --milestone vX.Y.Z --state all \
     --json number --jq '.[].number' \
   | xargs -I{} gh issue edit {} --repo CherryHQ/stella --add-label "release:vX.Y.Z"
   ```

   Filter the project board by the `release:vX.Y.Z` label to confirm the release scope.

9. Push the branch and new release tag explicitly: `git push origin main vX.Y.Z`.
10. CI triggers `.github/workflows/release.yml` → GoReleaser binaries + Docker images.

## Update Changelog

The changelogs live at `web/content/docs/changelog.mdx` and `web/content/docs/changelog.zh.mdx` (rendered on the docs site).
They have YAML frontmatter — preserve it when editing. Only modify content below the `---` block, and keep English and Chinese entries in sync.

Gather changes since last tag:

```bash
git log $(git describe --tags --abbrev=0 2>/dev/null || git rev-list --max-parents=0 HEAD)..HEAD --oneline
gh pr list --state merged --base main --search "merged:>=$(git log -1 --format=%aI $(git describe --tags --abbrev=0 2>/dev/null || git rev-list --max-parents=0 HEAD))"
```

Apply to both changelog files:

1. Rename `[Unreleased]` to `[X.Y.Z] - YYYY-MM-DD`
2. Add fresh `[Unreleased]` section above
3. Categorize: `✨ Features`, `🐛 Bug Fixes`, `♻️ Refactoring`, `📝 Documentation`, `📦 Dependencies`
4. Link PRs: `([#123](https://github.com/CherryHQ/stella/pull/123))`
5. Append: `**Full Changelog**: [vPREV...vX.Y.Z](https://github.com/CherryHQ/stella/compare/vPREV...vX.Y.Z)`

## Validate and Test

The standard pre-commit gate (`mise run format && mise run build && mise run test`) must already be green before cutting a release.

```bash
VERSION=X.Y.Z
test "$(jq -r '.version' web/package.json)" = "$VERSION"
mise run release:check     # Validate .goreleaser.yaml
mise run release:snapshot  # Test release locally (no tag needed)
```

## Artifacts

- **Binaries**: linux/darwin/windows × amd64/arm64 (GoReleaser)
- **Docker**: `ghcr.io/cherryhq/stella` — linux/amd64 + linux/arm64
- **Docker tags**: `latest` (stable), `vX.Y.Z` (release), SHA (every build)
