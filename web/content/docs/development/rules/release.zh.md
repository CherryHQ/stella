---
title: 发布
description: Stella 的发布打标与打包流程。
---

## Tag 格式

使用带 `v` 前缀的语义化版本：`v0.1.0`、`v1.0.0`、`v1.2.3-rc.1`。GoReleaser 会自动识别 `-rc.1`、`-beta.1` 这类预发布后缀。

## 发布渠道

Stella 有一个生产渠道和一个候选版本渠道：

- **Stable**：`vX.Y.Z`，发布到 GitHub、Homebrew、Linux 软件包、Docker `latest`，并进入生产 Helm 流程。
- **Release candidate**：`vX.Y.Z-rc.N`，作为 GitHub prerelease 和带完整版本号的 Docker 镜像发布，用于验证。它不能移动 `latest`、更新稳定 Homebrew tap，也不能成为 Helm 默认镜像。

RC 从维护中的 `release/vX.Y` 分支切出，依次发布 `rc.1`、`rc.2`，直到可以打最终的 `vX.Y.Z`。Dev snapshot 不属于这个流程。

## 分支模型

`main` 是默认开发分支。每个维护中的小版本使用 `release/vX.Y` 分支：从 `main` 切出后，RC 和 stable 都从该分支发布；实际修复先合并到 `main`，再精确 backport 到 release 分支。

维护中的 minor 分支是 patch 线，不是下一 minor 的暂存分支：

- 绝不将整个 `main` merge 或 rebase 到已有的 `release/vX.Y`。`vX.Y.Z`
  patch 只能包含该 minor 线选定的修复，通常是从 `main` 精确 backport 的提交。
- 如果发布必须包含当前完整的 `main` 文件树，应从 `main` 切出
  `release/vX.(Y+1)` 并发布 `vX.(Y+1).0`，不能在旧线递增 `Z` 来表示它。
- sync-back PR 只从 `release/vX.Y` 流向 `main`，用于回灌发布元数据与
  release-only 修复；它绝不授权把 `main` forward-merge 到维护中的 patch 线。

## 发布流程

1. 选择发布 tag `vX.Y.Z` 或 `vX.Y.Z-rc.N`；Web package 版本为去掉开头 `v` 的相同版本。
2. 新 minor 从选定的 `main` 提交切出 `release/vX.Y`；已有 patch 线只接收选定的 backport，不合入整个 `main`。从最新维护分支创建短期准备分支，发布元数据通过 PR 合入维护分支。
3. 更新 `web/package.json`，确保 `.version` 与 API/server 发布版本一致：
   ```bash
   VERSION=X.Y.Z
   tmp=$(mktemp)
   jq --arg version "$VERSION" '.version = $version' web/package.json > "$tmp" && mv "$tmp" web/package.json
   test "$(jq -r '.version' web/package.json)" = "$VERSION"
   ```
4. Stable 发布时，更新 `deploy/helm/stella/Chart.yaml` 中的 Helm chart 元数据：
   - 将 `appVersion` 设为 `"vX.Y.Z"`，记录 chart 同期交付的应用版本。默认 `image.tag` 为 `latest`，CI 会在每个稳定版本发布它，因此全新安装会直接使用该版本；`appVersion` 只负责保持元数据准确。
   - 如果 chart 自上次发布后发生变化，递增 chart 自身独立的 SemVer `version`。

   RC 不更新或发布稳定 Helm chart。RC 部署必须显式将 `image.tag` 固定为完整的候选版本 tag。

5. 更新 `web/content/docs/changelog.mdx` 和 `web/content/docs/changelog.zh.mdx`。
6. 提交：`📝 docs: Update CHANGELOG for vX.Y.Z`，包含两份 changelog 和 `web/package.json`；Stable 发布还包含 `deploy/helm/stella/Chart.yaml`。
7. 运行下文的 `mise run release:validate` 完整发布前门禁，确认发布提交是 `HEAD` 且工作区干净：
   ```bash
   git status --short
   git log --oneline -1
   ```
   如果发布提交不是 `HEAD`，立即停止；提交存在前绝不打 tag。
8. 确认版本 Milestone 包含准确的发布范围，且没有未关闭的 Issue。存在未关闭 Issue 时停止，不要打 tag：
   ```bash
   gh issue list --repo CherryHQ/stella --milestone vX.Y.Z --state open
   gh issue list --repo CherryHQ/stella --milestone vX.Y.Z --state all
   ```
9. 推送准备分支，向 `release/vX.Y` 创建 PR，等待必要检查通过后合并。GitHub 不会因合入非默认分支而自动关闭关联 Issue，需显式关闭发布 Issue，并重新确认 Milestone 没有未完成范围。
10. 获取已合并的维护分支，为远端最新提交打 tag 后推送：
    ```bash
    RELEASE_BRANCH=release/vX.Y
    git fetch origin --prune
    git switch "$RELEASE_BRANCH"
    git reset --hard "origin/$RELEASE_BRANCH"
    TAG=vX.Y.Z # 或 vX.Y.Z-rc.N
    git tag "$TAG"
    test "$(git rev-parse "$TAG")" = "$(git rev-parse origin/$RELEASE_BRANCH)"
    git push origin "$TAG"
    ```
11. CI 触发 `.github/workflows/release.yml`，先验证 tag 的确切提交属于对应维护分支，再运行验证与发布任务。
12. 所有发布 CI 任务成功且 GitHub Release 可见后，发布负责人必须完成[同步回 main](#同步回-main)。RC 和 Stable 都必须执行；只创建 PR 不算发布完成。
13. Stable 发布及回流完成后，关闭版本 Milestone。RC 保持 Milestone 开放，直到最终 Stable 发布：
    ```bash
    MILESTONE_NUMBER=$(gh api 'repos/CherryHQ/stella/milestones?state=open' \
      --jq '.[] | select(.title == "vX.Y.Z") | .number')
    test -n "$MILESTONE_NUMBER"
    gh api --method PATCH "repos/CherryHQ/stella/milestones/$MILESTONE_NUMBER" \
      -f state=closed
    ```

版本 Milestone 是持久的发布记录，不要再创建重复的 `release:vX.Y.Z` label。

## 同步回 main

发布负责人负责跟进回流直到合并。产物发布成功后，仍需完成回流；不能将 changelog
或 release-only 修复推迟到下一个版本。

1. 获取最新 `main` 和 release 分支，以已发布 tag 为固定来源，核对相对 `main`
   的变更，包括发布准备及为修通发布而新增的修复。此前回流或 backport 可能使用
   squash 或 cherry-pick，不能仅凭提交祖先关系判断内容是否已经存在。
2. 创建以 `main` 为目标的 PR。只有完整 diff 都是预期改动时，才直接使用 release
   分支；否则从当前 `main` 创建短期 `sync/vX.Y.Z-to-main` 分支，移植 tag 中的
   相关改动。遵循 `project-tracker.zh.md` 和 PR 模板。
3. 逐项核对发布内容：
   - 补齐中英文 changelog 缺失的已发布版本章节。保留 `main` 后续工作的
     `Unreleased` 条目，只移除确认属于已发布版本的条目。禁止整文件覆盖，也不能
     把版本边界整理推迟到未来发布。
   - 同步 `web/package.json` 和适用的 Stable Helm 元数据，但不能降低 `main`
     上更新的版本或覆盖后续 chart 改动。RC 回流不能更改稳定 Helm 元数据。
   - 移植 release-only 的代码、构建和文档修复。每项修复都要注明对应的 main
     提交或关联 PR，不适用的则解释原因。
4. 在同步分支解决冲突，保留 `main` 后续改动。按变更范围完成必要检查，经审阅后
   合并 PR。单独提交的修复 PR 也必须合并，才能完成发布收尾。禁止为解决回流冲突
   而把整个 `main` 合入维护中的 release 分支。
5. 核实合并后的 `main` 已包含双语发布记录、适用元数据及全部适用的 release-only
   修复。在发布准备 PR 中记录已发布 tag、已合并的同步和修复 PR 链接。如果内容
   已全部存在，记录对应 main 提交作为依据，不创建空 PR。

回流失败、存在冲突或 PR 未合并就被关闭时，状态为“已发布，回流未完成”。保持
Milestone 开放，在发布准备 PR 中记录剩余工作，完成后再收尾。不能为修复仅涉及
main 的回流问题而移动或重建已发布 tag。

## 更新 Changelog

Changelog 位于 `web/content/docs/changelog.mdx` 和 `web/content/docs/changelog.zh.mdx`，并渲染在文档站点。文件带 YAML frontmatter；编辑时保留该区块，只修改 `---` 之后的正文，并保持中英文条目同步。

收集上一个 tag 之后的变更：

```bash
git log $(git describe --tags --abbrev=0 2>/dev/null || git rev-list --max-parents=0 HEAD)..HEAD --oneline
gh pr list --state merged --base "${RELEASE_BRANCH:-main}" --search "merged:>=$(git log -1 --format=%aI $(git describe --tags --abbrev=0 2>/dev/null || git rev-list --max-parents=0 HEAD))"
```

同时更新两份 changelog：

1. 将 `[Unreleased]` 改为 `[X.Y.Z] - YYYY-MM-DD`。
2. 在其上方新增空的 `[Unreleased]`。
3. 按 `✨ Features`、`🐛 Bug Fixes`、`♻️ Refactoring`、`📝 Documentation`、`📦 Dependencies` 分类。
4. 链接 PR：`([#123](https://github.com/CherryHQ/stella/pull/123))`。
5. 追加 `**Full Changelog**: [vPREV...vX.Y.Z](https://github.com/CherryHQ/stella/compare/vPREV...vX.Y.Z)`。

## 验证与测试

运行完整发布前门禁。它严格按 `format` → `build` → `build:web` → `test` → `release:check` → `release:snapshot` 执行：

```bash
VERSION=X.Y.Z # 或 X.Y.Z-rc.N
test "$(jq -r '.version' web/package.json)" = "$VERSION"
mise run release:validate
```

本地门禁顺序执行，任一步失败都会停止后续工作。tag 工作流先验证发布来源，再并行运行质量检查与构建、Go/Web 测试、System Test 和 release snapshot；GoReleaser 与主 Docker 镜像发布必须等待四路全部通过。发布 CI 使用固定的 Ubuntu runner，System Test 在独立工作区中以 `if: always()` 上传测试日志。详见 `testing.zh.md`。

## Agent 性能门禁

Terminal-Bench 2.1 是按风险决定的手动发布门禁，不是每个 RC 或 Stable patch
都必须跑的检查。当发布决策需要 Agent 行为证据时运行它，尤其是改动了工具、prompt、
runner loop、面向模型的能力或沙箱行为之后，或者 release owner 明确要求时。
`mise run release:validate` 仍是必须执行的本地 pre-cut gate，并且有意不承担在线
模型与一次性 AWS 的成本。

```bash
CANDIDATE=$(git rev-parse HEAD)
mise run eval:tb21:aws -- --commit "$CANDIDATE"
```

一旦决定运行评估，只有 89 道题都选满 5 个 scoreable trial、脱敏 archive 与
checksum 验证通过、云资源清理完成，才可以打 tag 或发布。将结果归档到
`test/evals/harbor/results/terminal-bench-2.1/`，metadata 必须记录被测 commit。
若之后再改动影响 agent 的代码，必须针对新 candidate 重跑。release PR 必须记录
评估证据，或说明为何不需要评估。

每次发布记录首先对照**上一个 Stella release**，用于观察版本间变化。只有 model、
gateway、dataset、host、timeout、harness 与 capability treatment 一致时，才可作为
因果证据（`harness` 指跑它的 agent，`treatment` 指它被允许做什么），
否则必须标为描述性背景。Pi 等其他 agent 可以作为可选参考 baseline（`--baseline Pi`
会在每个它测过的 metric 上画一条虚线参考线），但绝不是 release KPI，也不暗示产品
目标：与 baseline 的差距只是读刻度用的背景，追平它不是发布要求。

不凭空设定一个回归阈值来静默阻塞发布。发布 PR 必须写清比较、解释变化，并记录
明确的发布决定。scoreboard 和 archive 规则见
`test/evals/harbor/results/README.md`。

## 发布工件

- **二进制**：linux/darwin × amd64/arm64（GoReleaser）。Windows 仅保留 compile-only 可移植性覆盖，不是发布的服务端目标。
- **Docker**：`ghcr.io/cherryhq/stella` — linux/amd64 + linux/arm64
- **Docker tags**：`latest`（仅稳定版）、`vX.Y.Z`（稳定版）、`vX.Y.Z-rc.N`（RC）、SHA（每次构建）
