---
title: 发布
description: Stella 的发布打标与打包流程。
---

## Tag 格式

使用带 `v` 前缀的语义化版本：`v0.1.0`、`v1.0.0`、`v1.2.3-rc.1`。GoReleaser 会自动识别 `-rc.1`、`-beta.1` 这类预发布后缀。

## 发布流程

1. 选择发布 tag `vX.Y.Z`；Web package 版本为不带 `v` 的 `X.Y.Z`。
2. 更新 `web/package.json`，确保 `.version` 与 API/server 发布版本一致：
   ```bash
   VERSION=X.Y.Z
   tmp=$(mktemp)
   jq --arg version "$VERSION" '.version = $version' web/package.json > "$tmp" && mv "$tmp" web/package.json
   test "$(jq -r '.version' web/package.json)" = "$VERSION"
   ```
3. 更新 `deploy/helm/stella/Chart.yaml` 中的 Helm chart 元数据：
   - 将 `appVersion` 设为 `"vX.Y.Z"`，记录 chart 同期交付的应用版本。默认 `image.tag` 为 `latest`，CI 会在每个稳定版本发布它，因此全新安装会直接使用该版本；`appVersion` 只负责保持元数据准确。
   - 如果 chart 自上次发布后发生变化，递增 chart 自身独立的 SemVer `version`。
4. 更新 `web/content/docs/changelog.mdx` 和 `web/content/docs/changelog.zh.mdx`。
5. 提交：`📝 docs: Update CHANGELOG for vX.Y.Z`，包含两份 changelog、`web/package.json` 和 `deploy/helm/stella/Chart.yaml`。
6. 确认提交成功且工作区干净：
   ```bash
   git status --short
   git log --oneline -1
   ```
   如果发布提交不是 `HEAD`，立即停止；提交存在前绝不打 tag。
7. 确认版本 Milestone 包含准确的发布范围，且没有未关闭的 Issue。存在未关闭 Issue 时停止，不要打 tag：
   ```bash
   gh issue list --repo CherryHQ/stella --milestone vX.Y.Z --state open
   gh issue list --repo CherryHQ/stella --milestone vX.Y.Z --state all
   ```
8. 为发布提交打 tag，并验证 tag 指向 `HEAD`：
   ```bash
   git tag vX.Y.Z
   test "$(git rev-parse vX.Y.Z)" = "$(git rev-parse HEAD)"
   ```
9. 显式推送分支与新 tag：`git push origin main vX.Y.Z`。
10. CI 触发 `.github/workflows/release.yml`。其中的 validation job 会先验证 tag 指向的确切提交，之后 GoReleaser 和 Docker 发布 job 才能开始。
11. CI 成功且 GitHub Release 可见后，关闭版本 Milestone：
    ```bash
    MILESTONE_NUMBER=$(gh api 'repos/CherryHQ/stella/milestones?state=open' \
      --jq '.[] | select(.title == "vX.Y.Z") | .number')
    test -n "$MILESTONE_NUMBER"
    gh api --method PATCH "repos/CherryHQ/stella/milestones/$MILESTONE_NUMBER" \
      -f state=closed
    ```

版本 Milestone 是持久的发布记录，不要再创建重复的 `release:vX.Y.Z` label。

## 更新 Changelog

Changelog 位于 `web/content/docs/changelog.mdx` 和 `web/content/docs/changelog.zh.mdx`，并渲染在文档站点。文件带 YAML frontmatter；编辑时保留该区块，只修改 `---` 之后的正文，并保持中英文条目同步。

收集上一个 tag 之后的变更：

```bash
git log $(git describe --tags --abbrev=0 2>/dev/null || git rev-list --max-parents=0 HEAD)..HEAD --oneline
gh pr list --state merged --base main --search "merged:>=$(git log -1 --format=%aI $(git describe --tags --abbrev=0 2>/dev/null || git rev-list --max-parents=0 HEAD))"
```

同时更新两份 changelog：

1. 将 `[Unreleased]` 改为 `[X.Y.Z] - YYYY-MM-DD`。
2. 在其上方新增空的 `[Unreleased]`。
3. 按 `✨ Features`、`🐛 Bug Fixes`、`♻️ Refactoring`、`📝 Documentation`、`📦 Dependencies` 分类。
4. 链接 PR：`([#123](https://github.com/CherryHQ/stella/pull/123))`。
5. 追加 `**Full Changelog**: [vPREV...vX.Y.Z](https://github.com/CherryHQ/stella/compare/vPREV...vX.Y.Z)`。

## 验证与测试

运行完整发布前门禁。它严格按 `format` → `build` → `test` → `system-test` → `release:check` → `release:snapshot` 执行：

```bash
VERSION=X.Y.Z
test "$(jq -r '.version' web/package.json)" = "$VERSION"
mise run release:validate
```

System Test 同时在本地门禁和 tag 触发的 validation job 中运行。发布 CI 固定使用支持的 Ubuntu runner，并在 snapshot build 清理 `dist/` 之前上传测试服务器日志。GoReleaser 与 Docker 发布 job 直接依赖 validation 结果，因此失败或不受支持的 System Test 无法发布部分工件。详见 `system-test.zh.md`。

## 发布工件

- **二进制**：linux/darwin × amd64/arm64（GoReleaser）。Windows 仅保留 compile-only 可移植性覆盖，不是发布的服务端目标。
- **Docker**：`ghcr.io/cherryhq/stella` — linux/amd64 + linux/arm64
- **Docker tags**：`latest`（稳定版）、`vX.Y.Z`（发布版）、SHA（每次构建）
