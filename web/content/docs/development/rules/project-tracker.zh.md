---
title: 项目跟踪
description: Stella 的飞书计划与 GitHub 执行流程。
---

Stella 使用两个职责分离的跟踪系统：

- **飞书多维表格**负责内部计划：Roadmap、产品里程碑、候选与已承诺任务、优先级、目标日期和交付负责人。
- **GitHub** 负责公开与研发记录：社区需求入口、Issue 详情、技术讨论、Assignee、PR 和发布范围。

不要创建 GitHub Project，也不要让同一个可编辑字段同时存在于两套系统。GitHub Issue 没有飞书任务，只表示团队尚未承诺交付，不表示 Issue 无效。

计划表为 [Stella Roadmap](https://mcnnox2fhjfq.feishu.cn/base/BEEbbI9jtad6PmsYSXpcmBy2nUd?table=tbl4pUhlngTJdg2Z&view=vewBhCvlG1)。

## 职责归属

| 内容                                    | 事实源                   | 说明                             |
| --------------------------------------- | ------------------------ | -------------------------------- |
| Roadmap 与产品方向                      | 飞书 Roadmap             | 内部计划，不复制到 GitHub        |
| 产品里程碑与验收                        | 飞书 Milestones          | 面向成果，可以跨多个版本         |
| 候选工作、承诺、优先级、日期、DRI、依赖 | 飞书 Tasks               | Task 进入 `就绪` 时代表团队承诺  |
| 公开需求与技术讨论                      | GitHub Issue             | 社区 Issue 在承诺前只留在 GitHub |
| 实现过程与当前开发者                    | GitHub Issue 与 PR       | 使用 Assignee、链接和评论        |
| 发布范围                                | GitHub Release Milestone | 只使用 `v0.61.0` 这类版本名      |
| 代码交付                                | GitHub PR                | PR 必须关联 Issue                |

飞书 Task 的 DRI 对交付结果负责；GitHub Assignee 是当前实现者，两者可以不是同一个人。

## Milestone 语义

产品里程碑与发布里程碑刻意分开：

- **飞书产品里程碑**描述成果，例如“支持多模态模型”。
- **GitHub 发布里程碑**描述版本，例如 `v0.61.0`。

不要同步两者，也不要复用名称。目标版本明确后，记录在关联 GitHub Issue 的发布 Milestone 上。不要创建主题型 GitHub Milestone 或 `release:*` label。

## 飞书结构

```text
Roadmap
  └── Milestones
        └── Tasks ── GitHub Issue URL
```

严格使用下面的标准字段和生命周期，不添加同义或一次性状态：

- Roadmap：`战线`、`一句话方向`、`状态`（`候选`、`进行中`、`暂停`、`已完成`）、`DRI`、`里程碑`。
- Milestones：`里程碑`、`状态`（`候选`、`已计划`、`进行中`、`暂停`、`已完成`、`已取消`）、`目标与验收`、`DRI`、`目标日期`、`预计人周`、`所属战线`、`任务`。
- Tasks：`任务`、`状态`（`待评估`、`就绪`、`进行中`、`阻塞`、`已完成`、`已取消`）、`优先级`、`DRI`、`里程碑`、`GitHub Issue`、`验收标准`、日期、估算、`父任务`、`依赖任务`、`依赖说明`、`触发条件`、产品线和引用。

`待评估` Task 是内部候选，不要求 GitHub Issue。进入 `就绪` 或后续状态的 Task 必须填写完整 GitHub Issue URL，不要只存 Issue number。评估阶段在飞书起草验收标准；晋级到 `就绪` 时，把完整需求和验收条件写入 GitHub，此后 Issue 是研发执行事实源。

## 社区 Issue 分流

Issue 表单会为新的社区报告添加 `status:needs-triage`。

```text
新 GitHub Issue
  ├── 无效 / 重复 / 问题咨询 → 解释后关闭或转到正确入口
  ├── 有效但未排期           → status:accepted；只留在 GitHub
  └── 已承诺                 → 飞书 Task 就绪 + status:ready
```

分流步骤：

1. 复现或澄清需求。
2. 添加合适的类型标签：`bug`、`enhancement`、`documentation` 或 `design`。
3. 移除 `status:needs-triage`。
4. 有效但未排期时添加 `status:accepted`，不要创建飞书 Task。
5. 确认承诺后，先在飞书和 GitHub 搜索重复项，再创建或关联飞书 Task，填入 GitHub Issue URL，将其改为 `就绪`，移除 `status:accepted`，添加 `status:ready`。
6. 只有明确目标发布版本后才添加版本 Milestone。

## 执行生命周期

| 事件           | 飞书 Task  | GitHub                                       |
| -------------- | ---------- | -------------------------------------------- |
| 候选           | `待评估`   | 不要求 Issue                                 |
| 已承诺并可开发 | `就绪`     | Open + `status:ready`                        |
| 开始开发       | `进行中`   | 移除 `status:ready`；分配 Assignee 或关联 PR |
| 阻塞           | `阻塞`     | 添加 `status:blocked` 并解释阻塞原因         |
| 实现关闭       | 验收前不变 | 通过 PR 关闭                                 |
| 验收通过       | `已完成`   | Closed                                       |
| 取消           | `已取消`   | 说明原因后关闭                               |

关闭 GitHub Issue 不会自动完成飞书 Task；DRI 验收后再将 Task 改为 `已完成`。

## 维护者计划的工作

未承诺的内部想法具体到可以估算或比较时，以 `待评估` Task 记录；不要创建猜想中的 GitHub Issue。团队承诺任务时：

1. 搜索是否已有对应的社区 Issue。
2. 有则直接关联，没有再创建。
3. 在飞书 Task 填入完整 Issue URL 和最终验收标准，再改为 `就绪`。
4. 为 Issue 添加 `status:ready`；目标版本明确时再添加版本 Milestone。

外部贡献者不需要访问飞书。接受社区工作时，由维护者完成飞书侧的晋级操作。

## GitHub Labels

项目管理标签刻意保持精简：

- 类型与结论：`bug`、`enhancement`、`documentation`、`design`、`duplicate`、`invalid`、`question`、`wontfix`。
- Intake 与执行：`status:needs-triage`、`status:accepted`、`status:ready`、`status:blocked`。
- 贡献者辅助：`good first issue`、`help wanted`。

`dependencies`、`go`、`javascript` 等自动化标签可以保留在生成的 PR 上。不要添加 priority label，优先级由飞书负责；不要添加 release label，版本 Milestone 就是发布记录。

常用操作：

```bash
gh issue list --repo CherryHQ/stella --label status:needs-triage
gh issue edit <number> --repo CherryHQ/stella \
  --remove-label status:needs-triage --add-label status:accepted
gh issue edit <number> --repo CherryHQ/stella \
  --remove-label status:accepted --add-label status:ready
gh issue edit <number> --repo CherryHQ/stella --milestone v0.61.0
```

## Issue 与 PR 约定

社区报告使用 Issue 表单。维护者创建的实现 Issue 使用 **What**、**Why**、**How** 和 **Refs**。计划变化时及时更新 Issue，不要把 Issue 评论复制到飞书。

每个 PR 都必须关联 GitHub Issue。PR 完成整个 Issue 时使用 `Closes #123`；只完成一部分时使用 `Refs #123`。完整填写 PR 模板中的 What、Why、How、Test 和 Refs。

## 代用户创建 Issue

代用户创建 Issue 前：

1. 起草并确认标题和正文。
2. 选择类型标签。
3. 确认工作只是已接受，还是已经承诺交付。
4. 仅接受时添加 `status:accepted`，然后结束。
5. 已承诺时确认飞书 Task 将关联新 Issue，并询问是否已有目标版本；`none` 是合法答案。

随后创建 Issue。已承诺的工作还要用返回的 URL 创建或更新飞书 Task，将其改为 `就绪`，并添加 `status:ready`；最后返回 Issue URL。未经确认不要批量创建；优先关闭而不是删除。

## 自动化边界

在反复出现明确痛点之前保持手工流程。初期仅考虑两项自动化：

1. 飞书 Task 进入 `就绪` 时创建或关联 Issue，并回写 URL；
2. 关联 Issue 关闭或重新打开时通知飞书 Task。

不要双向同步描述、评论、Assignee、优先级、日期、Milestone 或删除操作；这些字段已经各有唯一 owner。
