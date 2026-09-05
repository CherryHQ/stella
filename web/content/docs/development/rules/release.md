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

A maintained minor branch is a patch line, not a staging branch for the next
minor release:

- Never merge or rebase all of `main` into an existing `release/vX.Y` branch.
  A `vX.Y.Z` patch contains only selected fixes for that minor line, normally
  backported from `main`.
- If a release must contain the current `main` tree, cut `release/vX.(Y+1)`
  from `main` and release `vX.(Y+1).0`. Do not express that release by
  incrementing `Z` on the older line.
- A sync-back PR flows from `release/vX.Y` to `main` only for release metadata
  and release-only fixes. It never authorizes a forward merge from `main` into
  the maintained patch line.

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
13. After all release CI jobs succeed and the GitHub Release is visible, the
    release owner must complete [Sync back to main](#sync-back-to-main). Both RC
    and stable releases require this step. Opening a PR alone does not complete
    the release.
14. After the stable release and sync-back are complete, close the version
    milestone. For an RC, keep it open until the final stable release:
    ```bash
    MILESTONE_NUMBER=$(gh api 'repos/CherryHQ/stella/milestones?state=open' \
      --jq '.[] | select(.title == "vX.Y.Z") | .number')
    test -n "$MILESTONE_NUMBER"
    gh api --method PATCH "repos/CherryHQ/stella/milestones/$MILESTONE_NUMBER" \
      -f state=closed
    ```

The version milestone is the durable release record. Do not create a duplicate
`release:vX.Y.Z` label.

## Sync back to main

The release owner owns sync-back through merge. Publication alone does not
complete a release; do not defer changelogs or release-only fixes to the next
version.

1. Fetch the latest `main` and release branch, and use the published tag as the
   fixed source for the sync. Review its changes against `main`, including
   release preparation and any fixes made while getting publication to pass.
   Commit ancestry alone does not prove that content is missing or present:
   earlier syncs and backports may have been squashed or cherry-picked.
2. Open a PR targeting `main`. A direct release-branch PR is suitable only when
   its full diff contains the intended changes. Otherwise, create a short-lived
   `sync/vX.Y.Z-to-main` branch from current `main` and port the relevant changes
   from the tag. Follow `project-tracker.md` and the PR template.
3. Account for every part of the release:
   - Restore missing published version sections in both changelogs. Preserve
     later `main` work in `Unreleased`; remove only entries confirmed to belong
     to the published release. Never replace the whole file with the release
     copy or defer the version boundary to a future release.
   - Sync `web/package.json` and applicable stable Helm metadata without
     downgrading newer versions on `main` or overwriting newer chart changes.
     RC syncs must not change stable Helm metadata.
   - Port release-only code, build, and documentation fixes. For each fix,
     identify its main commit or linked PR, or explain why it does not apply.
4. Resolve conflicts on the sync branch while preserving later `main` changes.
   Run the checks required for the changes, obtain review, and merge the PR.
   If a fix uses a separate PR, that PR must also be merged before closing out
   the release. Never merge all of `main` into the maintained release branch
   to resolve sync-back conflicts.
5. Verify the merged `main` contains both language versions of the release
   notes, applicable metadata, and all applicable release-only fixes. Record
   the published tag and merged sync/fix PR links in the release preparation
   PR. If everything was already present, record the supporting main commits
   there instead of creating an empty PR.

If sync-back fails, conflicts, or its PR is closed without merging, the release
remains published with sync-back incomplete. Keep the milestone open, record the
remaining work in the release preparation PR, and finish it before closing out
the release. Do not move or recreate the published tag to repair a main-only
sync problem.

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

Run the full pre-cut gate in order: `format` → `build:embedded` → `test` →
`release:check` → `release:snapshot`. `build:embedded` refreshes generated code
and the SPA; `test` then builds the server and runs Go, frontend, and system
tests with `STELLA_TEST_REQUIRE_SPA=1`.

```bash
VERSION=X.Y.Z # or X.Y.Z-rc.N
test "$(jq -r '.version' web/package.json)" = "$VERSION"
mise run release:validate
```

The local gate remains sequential. The tag workflow verifies the exact source,
then runs quality/build, package Go/Web tests, subprocess system tests, and main
and sandbox image builds in parallel. Each test suite runs once. Images are
uploaded by digest only; no version, latest, or sandbox compatibility alias moves
at this stage. Failed runs may leave untagged candidate images and build caches.

After every validation and image build succeeds, the pinned GoReleaser builds and
publishes all four platforms once. Tag CI no longer builds a duplicate snapshot
first; the local pre-cut snapshot remains required. Main and sandbox aliases are
published only after GoReleaser succeeds. Mirror transfer copies the manifest to
the destination once, then creates its other aliases from that destination.

Publishing across GitHub, Homebrew, GHCR, and the mirror is not transactional.
If a publication step fails, retain the tag, inspect which artifacts already
exist, and rerun only the failed jobs for that same run. A failed validation never
permits publishing aliases; a failed mirror copy does not make the release
complete. Do not retag to repair publication. Finish verification and sync-back
before closing the milestone.

The Release workflow runs only for `v*.*.*` tag pushes. PRs use the ordinary
checks described in [Testing](./testing); there is no PR or manual release
rehearsal workflow. Changes to CI policy are checked by `mise run test:ci-policy`
in both the Format job and release quality job. System logs are uploaded even
on failure.

Use `mise tasks` to inspect the available commands:

| Task               | Purpose                                                                                                                                                            |
| ------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `release:check`    | Validate GoReleaser configuration without building or publishing.                                                                                                  |
| `release:snapshot` | Build Linux/macOS × amd64/arm64 archives and Linux packages without publishing; verify the host archive contains `stellad`. Requires one of those supported hosts. |
| `release:validate` | Run the complete local pre-cut gate above, stopping at the first failure.                                                                                          |
| `release`          | Build and publish through GoReleaser. Tag CI calls this after source, quality, test, and image gates; it does not run those gates itself.                          |

The local snapshot remains a release validation tool. Its duration excludes
registry uploads and mirror transfer. Cache timing must distinguish restored
branch caches from cold builds.

## Agent Performance Gate

Terminal-Bench 2.1 is a manual, risk-based release gate, not a required check
for every release candidate or stable patch. Run it when the release decision
needs evidence about agent behavior — in particular after changes to tools,
prompts, the runner loop, model-facing capabilities, or sandbox behavior — or
when the release owner explicitly requests it. `mise run release:validate`
remains the required local pre-cut gate and intentionally does not spend the
live-model and disposable-AWS budget.

```bash
CANDIDATE=$(git rev-parse HEAD)
mise run eval:tb21:aws -- --commit "$CANDIDATE"
```

When an evaluation is run, do not tag or publish until it has exactly five
selected scoreable trials for each of the 89 tasks, its redacted archive and
checksums verify, and cloud cleanup completes. Archive the result under
`test/evals/harbor/results/terminal-bench-2.1/` with the evaluated commit in
its metadata. If agent-affecting code changes after the run, rerun it for the
new candidate. The release PR records either the evidence or why evaluation
was not needed.

Every release record first compares Stella with the prior Stella release to
show version-to-version movement. A comparison is causal evidence only when
model, gateway, dataset, host, timeout, harness, and capability treatment match
(`harness` names the agent that ran it, `treatment` names what it was allowed
to do); otherwise
label it descriptive context. Other agents, including Pi, may appear as optional
reference baselines (`--baseline Pi` draws one on every metric it measured),
never as the release KPI or an implied product target: a gap to a baseline is
context for reading the scale, and closing it is not a release requirement.

A regression does not silently block a release by an invented threshold. The
release PR must state the comparison, explain any movement, and record the
explicit release decision. See `test/evals/harbor/results/README.md` for the
scoreboard and artifact rules.

## Artifacts

- **Binaries**: linux/darwin × amd64/arm64 (GoReleaser). Windows remains compile-only portability coverage, not a published server target.
- **Docker**: `ghcr.io/cherryhq/stella` — linux/amd64 + linux/arm64
- **Docker tags**: `latest` (stable only), `vX.Y.Z` (stable), `vX.Y.Z-rc.N` (RC), SHA (every build)
