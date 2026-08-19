---
title: 项目跟踪
description: Stella 的飞书计划与 GitHub 执行流程。
---

Stella 使用两个职责分离的跟踪系统：

- **飞书多维表格**负责内部计划：Roadmap、产品里程碑、候选与已承诺任务、优先级、目标日期和交付负责人。
- **GitHub** 负责公开与研发记录：社区需求入口、Issue 详情、技术讨论、Assignee、PR 和发布范围。

不要创建 GitHub Project，也不要让同一个可编辑字段同时存在于两套系统。

计划只朝一个方向流动：飞书 Task 被承诺时创建或关联 GitHub Issue，因此已承诺的 Task 一定有 Issue。反向不做要求——只有 Issue 没有飞书 Task 是正常且合法的，无论它来自社区还是维护者先写了代码。周二的交付复盘会把这些 Issue 统一回填成 Task。不要为了"先建 Task"而卡住工作。

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

### 坐标

直接使用下列 id，不要再从 Base URL 里解析或猜测。

| 资源       | Id                            |
| ---------- | ----------------------------- |
| Base token | `BEEbbI9jtad6PmsYSXpcmBy2nUd` |
| 任务       | `tbl4pUhlngTJdg2Z`            |
| 里程碑     | `tblCRcuKDjmnKCJr`            |
| Roadmap    | `tblp9iIcKyO9NN00`            |

### 字段

`select` 列出的是全部合法取值。`user` 字段填 `[{"id":"ou_..."}]`，用
`lark-cli contact +get-user` 解析（省略 user id 即查自己）。`link` 字段填
`[{"id":"recXXXXXXXXXX"}]`，指向"关联"列所示表中的记录——先读那张表拿到 record id，
不要凭空构造。

**任务**

| 字段                   | 类型     | 取值 / 关联                                               |
| ---------------------- | -------- | --------------------------------------------------------- |
| `任务`                 | text     | 任务标题                                                  |
| `状态`                 | select   | `待评估`、`就绪`、`进行中`、`阻塞`、`已完成`、`已取消`    |
| `优先级`               | select   | `P0`、`P1`、`P2`                                          |
| `产品线`               | select   | `数字员工`、`数字分身`、`平台核心`、`渠道`、`Web`、`运维` |
| `DRI`                  | user     | —                                                         |
| `里程碑`               | link     | 里程碑表                                                  |
| `父任务`               | link     | 任务表                                                    |
| `依赖任务`             | link     | 任务表                                                    |
| `GitHub Issue`         | text     | 完整 URL，不是 number                                     |
| `验收标准`             | text     | 验收条件                                                  |
| `触发条件`             | text     | `待评估` 任务的解锁条件                                   |
| `依赖说明`             | text     | —                                                         |
| `Refs`                 | text     | —                                                         |
| `开始日期`、`截止日期` | datetime | `YYYY-MM-DD HH:MM:SS`                                     |
| `估算(人/天)`          | number   | —                                                         |

**里程碑**

| 字段         | 类型     | 取值 / 关联                                            |
| ------------ | -------- | ------------------------------------------------------ |
| `里程碑`     | text     | 里程碑名称                                             |
| `状态`       | select   | `候选`、`已计划`、`进行中`、`暂停`、`已完成`、`已取消` |
| `目标与验收` | text     | —                                                      |
| `DRI`        | user     | —                                                      |
| `所属战线`   | link     | Roadmap 表                                             |
| `任务`       | link     | 任务表                                                 |
| `目标日期`   | datetime | —                                                      |
| `预计人周`   | number   | —                                                      |

**Roadmap**

| 字段         | 类型   | 取值 / 关联                        |
| ------------ | ------ | ---------------------------------- |
| `战线`       | text   | 战线名称                           |
| `一句话方向` | text   | —                                  |
| `状态`       | select | `候选`、`进行中`、`暂停`、`已完成` |
| `DRI`        | user   | —                                  |
| `里程碑`     | link   | 里程碑表                           |

### 写入 Base

用 `lark-cli base`，带 `--as user`。写之前先确认字段，并用 `--dry-run` 预览。

```bash
BASE=BEEbbI9jtad6PmsYSXpcmBy2nUd
TASKS=tbl4pUhlngTJdg2Z

# 构造 payload 前先确认线上字段
lark-cli base +field-list --base-token $BASE --table-id $TASKS --as user

# 创建任务（去掉 --dry-run 才真正执行）
lark-cli base +record-upsert --base-token $BASE --table-id $TASKS --as user --dry-run \
  --json '{"任务":"...","状态":"就绪","优先级":"P2","产品线":"平台核心",
           "GitHub Issue":"https://github.com/CherryHQ/stella/issues/123",
           "DRI":[{"id":"ou_..."}],"验收标准":"..."}'

# 给已有任务回写 Issue URL
lark-cli base +record-upsert --base-token $BASE --table-id $TASKS --as user \
  --record-id recXXXXXXXXXX --json '{"GitHub Issue":"https://github.com/CherryHQ/stella/issues/123"}'
```

`+record-upsert` 不带 `--record-id` 是创建，带上是更新；它不会按业务键匹配。
它的响应不回显写入后的行，所以要用 `+record-search` 回读校验，不要只看退出状态。

### 生命周期规则

`待评估` Task 是内部候选，不要求 GitHub Issue。进入 `就绪` 或后续状态的 Task 必须填写完整 GitHub Issue URL，不要只存 Issue number。这条约束只作用于 Task → Issue 方向；只有 Issue 没有 Task 不是需要当场补上的缺口。评估阶段在飞书起草验收标准；晋级到 `就绪` 时，把完整需求和验收条件写入 GitHub，此后 Issue 是研发执行事实源。

## 社区 Issue 分流

Issue 表单会为新的社区报告添加 `status:needs-triage`。

```text
新 GitHub Issue
  ├── 无效 / 重复 / 问题咨询 → 解释后关闭或转到正确入口
  ├── 有效但未排期           → status:accepted；只留在 GitHub
  └── 已承诺                 → status:ready；Task 可选，周二回填
```

分流步骤：

1. 复现或澄清需求。
2. 添加合适的类型标签：`bug`、`enhancement`、`documentation` 或 `design`。
3. 移除 `status:needs-triage`。
4. 有效但未排期时添加 `status:accepted`，不要创建飞书 Task。
5. 确认承诺后，移除 `status:accepted`，添加 `status:ready`。只有当下就要在飞书排期时才创建 Task，否则交给周二复盘回填。确实要建时，先在飞书和 GitHub 搜索重复项，填入 Issue URL，并改为 `就绪`。
6. 只有明确目标发布版本后才添加版本 Milestone。

## 执行生命周期

| 事件           | 飞书 Task  | GitHub                                       |
| -------------- | ---------- | -------------------------------------------- |
| 候选           | `待评估`   | 不要求 Issue                                 |
| 已承诺、未动工 | `就绪`     | Open + `status:ready`                        |
| PR 已开        | `进行中`   | 移除 `status:ready`；分配 Assignee 或关联 PR |
| 阻塞           | `阻塞`     | 添加 `status:blocked` 并解释阻塞原因         |
| 实现关闭       | 验收前不变 | 通过 PR 关闭                                 |
| 验收通过       | `已完成`   | Closed                                       |
| 取消           | `已取消`   | 说明原因后关闭                               |

飞书那一列只在 Task 存在时适用。只在 GitHub 上跟踪的工作本身就是完整合法的，Task 由周二复盘补建。

以"PR 已开"作为 `进行中` 的标志，因为它是唯一带客观时间戳的转换点。状态要如实
反映进度：建 Issue 时代码已经写完的工作直接进 `进行中`，不经过 `status:ready`。

关闭 GitHub Issue 不会自动完成飞书 Task；DRI 验收后再将 Task 改为 `已完成`。

## 维护者计划的工作

未承诺的内部想法具体到可以估算或比较时，以 `待评估` Task 记录；不要创建猜想中的 GitHub Issue。团队承诺任务时：

1. 搜索是否已有对应的社区 Issue。
2. 有则直接关联，没有再创建。
3. 在飞书 Task 填入完整 Issue URL 和最终验收标准，再改为与实际进度一致的状态——
   `就绪`，或 PR 已开时直接进 `进行中`。
4. 为 Issue 添加对应的状态 label；目标版本明确时再添加版本 Milestone。

从 GitHub 起步的工作走短路径：建 Issue、打标签、开做，周二复盘再把它变成 Task。

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

多数 PR 会关联 GitHub Issue。PR 完成整个 Issue 时使用 `Closes #123`；只完成一部分时使用 `Refs #123`。完整填写 PR 模板中的 What、Why、How、Test 和 Refs。

小修不需要 Issue。改动足够自洽、Reviewer 看 diff 就能判断时直接开 PR：错别字、一行 bug 修复、文档订正、测试修复。此时在 Refs 写 `No issue: <原因>` 代替 Issue 号。需要讨论、改变对外行为、或者跨多个模块的改动仍然先建 Issue——凡是 Reviewer 会想了解背景的都算。

## 代用户创建 Issue

代用户创建 Issue 前：

1. 起草并确认标题和正文。
2. 选择类型标签。
3. 选择状态标签：已接受但未排期用 `status:accepted`，已承诺但未动工用 `status:ready`，PR 已开则不加。

随后创建 Issue 并返回 URL。不要追问飞书 Task，周二复盘会处理。只有目标版本已经明确时才添加版本 Milestone。未经确认不要批量创建；优先关闭而不是删除。

## 每周交付复盘

每周二复盘刚结束的一周：收集已合并 PR，创建或刷新对应任务，汇报交付了什么。
交付周从周二算到下周二。没有飞书 Task 就交付了的 Issue 正是在这一步补上 Task，
所以前面的任何环节都不需要等 Task 建好。

流程固化在 `.agents/skills/weekly-delivery/` 的 `weekly-delivery` skill 里。
脚本负责机械部分——周窗口计算、PR 收集、区分 issue 号与 PR 号、与任务表比对
——并在每个判断点停下等人：任务名、状态、产品线、里程碑、验收标准。

任务用 `完成日期` 记录交付时点，取交付它的最后一个 PR 的 merge 日；`截止日期`
仍然是 deadline。`交付周` 与 `周次` 是基于 `完成日期` 的公式，因此 `上周交付`
视图和 `交付总览` 仪表盘会自动滚动。

交付写入经复核后，需要从每个交付 PR 回链对应的飞书 Task。只有完整 Issue 已随该
版本发布时，才设置其 GitHub 发布 Milestone。发布 tag 与 release 分支的 cherry-pick
才是依据，不能只按合入时间判断。这仍是 GitHub 的发布元数据，不是复制飞书产品
里程碑。

## 自动化边界

在反复出现明确痛点之前保持手工流程。初期仅考虑两项自动化：

1. 飞书 Task 进入 `就绪` 时创建或关联 Issue，并回写 URL；
2. 关联 Issue 关闭或重新打开时通知飞书 Task。

不要双向同步描述、评论、Assignee、优先级、日期、Milestone 或删除操作；这些字段已经各有唯一 owner。
