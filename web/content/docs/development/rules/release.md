---
title: Release
description: Release tagging and packaging workflow for Stella.
---

## Tag Format

Use semantic versioning with `v` prefix: `v0.1.0`, `v1.0.0`, `v1.2.3-rc.1`.
GoReleaser auto-detects pre-release suffixes (`-rc.1`, `-beta.1`).

## Release Channels

Stella has one production channel and one candidate channel:

- **Stable**: `vX.Y.Z`, published to GitHub, Homebrew, Linux packages, Docker
  `latest`, and the production Helm flow.
- **Release candidate**: `vX.Y.Z-rc.N`, published as a GitHub prerelease and
  versioned Docker images for validation. It must not move `latest`, update the
  stable Homebrew tap, or become the default Helm image.

RCs are cut from the maintained `release/vX.Y` branch. Publish `rc.1`, `rc.2`,
and so on until the branch is ready for the final `vX.Y.Z` tag. Dev snapshots
are not part of this workflow.

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

1. Choose release tag `vX.Y.Z` or `vX.Y.Z-rc.N`; the web package version is the same version without the leading `v`.
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
5. For a stable release, update the Helm chart metadata in `deploy/helm/stella/Chart.yaml`:
   - Set `appVersion: "vX.Y.Z"` so the chart records the release it ships alongside.
     The default `image.tag` is `latest` (CI publishes it for every stable release,
     see Artifacts), so a fresh install already tracks this version; `appVersion`
     just keeps the metadata honest.
   - Bump the chart's own `version` (its SemVer, independent of `appVersion`)
     whenever the chart changed since the last release.

   For an RC, do not update or publish the stable Helm chart. RC operators must
   pin `image.tag` to the full candidate tag explicitly.

6. Update `web/content/docs/changelog.mdx` and `web/content/docs/changelog.zh.mdx` (see below).
7. Commit: `📝 docs: Update CHANGELOG for vX.Y.Z` including both changelogs and
   `web/package.json`; stable releases also include `deploy/helm/stella/Chart.yaml`.
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
    TAG=vX.Y.Z # or vX.Y.Z-rc.N
    git tag "$TAG"
    test "$(git rev-parse "$TAG")" = "$(git rev-parse origin/$RELEASE_BRANCH)"
    git push origin "$TAG"
    ```
12. CI triggers `.github/workflows/release.yml`. It verifies the exact tagged
    commit and the matching maintained branch before publication starts.
13. After CI succeeds and the GitHub Release is visible, open a sync-back PR
    from `release/vX.Y` to `main` when the release branch contains commits not
    already present on `main`. For an RC, keep the milestone open until the
    final stable release.
14. After the stable release only, close the version milestone:
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
VERSION=X.Y.Z # or X.Y.Z-rc.N
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

## Agent Performance Gate

Before every release candidate and stable release, run the complete
Terminal-Bench 2.1 evaluation against the proposed release commit. This is a
manual release gate because it needs a live model gateway and disposable AWS
compute; `mise run release:validate` intentionally does not spend that budget.

```bash
CANDIDATE=$(git rev-parse HEAD)
mise run eval:tb21:aws -- --commit "$CANDIDATE"
```

Do not tag or publish until the run has exactly five selected scoreable trials
for each of the 89 tasks, its redacted archive and checksums verify, and cloud
cleanup completes. Archive the result under
`test/evals/harbor/results/terminal-bench-2.1/` with the evaluated commit in
its metadata. If agent-affecting code changes after the run, rerun it for the
new candidate.

Every release record compares Stella with both baselines:

- **Pi** is the performance target. State Stella's resolution and `pass^5`
  gap to the current complete Pi run.
- **The prior Stella release** shows version-to-version movement. It is causal
  evidence only when model, gateway, dataset, host, timeout, and harness
  treatment match; otherwise label it descriptive context.

A regression does not silently block a release by an invented threshold. The
release PR must state the comparison, explain any movement, and record the
explicit release decision. See `test/evals/harbor/results/README.md` for the
scoreboard and artifact rules.

## Artifacts

- **Binaries**: linux/darwin × amd64/arm64 (GoReleaser). Windows remains compile-only portability coverage, not a published server target.
- **Docker**: `ghcr.io/cherryhq/stella` — linux/amd64 + linux/arm64
- **Docker tags**: `latest` (stable only), `vX.Y.Z` (stable), `vX.Y.Z-rc.N` (RC), SHA (every build)
