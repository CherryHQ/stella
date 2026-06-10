---
title: 后台任务
---

Stella 可以在后台运行可追踪的工作，你可以继续聊天。任务会在重启后保留：即使 Stella 重启，task 状态、run 历史、blocker 和 event 也仍然在数据库里。

## 什么是任务？

Task 是当前对话之外的一个可执行工作单元。当工作需要时间、需要可重试的运行历史、可能暂停等待输入，或需要 review 时，就使用 task。

每个 task 都有生命周期：

- **草稿** -- 已创建但尚未激活。
- **就绪** -- 在依赖关系和调度条件满足后可以被派发。
- **运行中** -- Stella 正在处理。
- **已阻塞** -- Stella 因为需要输入、外部依赖或策略原因而暂停。
- **审核中** -- Stella 已提交输出，正在等待配置的 review 决策。
- **已完成** -- 成功完成。
- **已失败** -- 重试预算耗尽、worker 报告不可重试失败，或 review 被拒绝。
- **已取消** -- 你停止了任务。

`Ready` 不等于“现在正在运行”。一个 task 仍可能在等待依赖、未来开始时间、worker 容量或执行 Agent。任务详情会显示 readiness 原因。

## 创建任务

如果工作是独立的，就创建 standalone task。如果多个 task 属于同一个大目标，先创建 goal，再把 child tasks 挂到它下面。每个 task 都属于一个 Agent，并在创建时获得自己的 durable worker session；你也可以把它挂到 goal 或 project。

典型 goal 工作流：

1. 创建 goal。
2. 用 goal ID 创建子 tasks。如果 task 没有传 agent ID，它会继承 goal 的 Agent。
3. 在有顺序要求时添加任务依赖。
4. 激活 goal 和 tasks。
5. 通过 events、blockers、runs 和 reviews 查看进展。

本版本不包含自动 goal 拆分。如果你需要多步骤工作流，请从 Web UI、CLI 或 Agent 命令显式创建子 tasks。

## 查看进度

你可以随时查看工作状态：

- **任务 facet** -- 打开某个 Agent 后选择**任务**，在同一处查看 tasks、定时工作和 goals。
- **任务详情** -- 查看 readiness、事件历史、runs、blockers、reviews 和依赖。
- **Goal 详情** -- 查看 child tasks 和 goal 的汇总状态。
- **Project 任务列表** -- 打开 project 会先看到该 project 的 tasks；需要项目级持久对话或 workspace 时，再进入对话和文件 facet。
- **Task sessions** -- 需要查看执行背后的 worker 对话时，从 task 或 run 的 session 链接进入。

根据你的渠道设置，任务完成、失败、阻塞或需要 review 时，Stella 可以通知你。

## 回复 blocker

当 Stella 无法安全继续时，task 会进入**已阻塞**状态。Blocker 会说明需要什么。

常见 blocker：

- 缺少用户输入。
- 外部依赖尚不可用。
- 工具或服务错误。
- 策略暂停。
- 上游依赖失败。

普通 blocker 可以通过回答问题来解决。失败依赖 blocker 不同：你必须带理由 waiver 该依赖，下游 task 才能继续。

## Reviews

当前 worker runtime 支持这些 task review policy：

- **none** -- 立即接受输出，task 变为 done。
- **auto** -- Stella 写入一条自动批准记录用于审计，然后将 task 标记为 done。
- **human** -- Stella 等待人工批准、拒绝或要求修改。

本版本不提供由 Agent 执行的 review。需要判断或审批时，请使用 human review。

## Goals

Goal 是一组相关 tasks 的容器。它从 child task 状态汇总：

- 所有必需子任务完成 → goal done。
- 任一必需子任务失败 → goal failed。
- 任一必需子任务阻塞 → goal blocked。
- 仍有子任务未完成 → goal 保持进行中。

被阻塞的 goal 会自动恢复：当你解决子任务的 blocker（或 waive 其失败依赖）后，goal 会自动回到进行中，无需单独的 goal-unblock 操作。

本版本会拒绝 goal 最终综合输出和 goal review。请把 goal 当作容器使用，并显式创建 child tasks。

## Worker 如何完成任务

当 Stella 派发一个 task worker 时，worker 必须用一个终止结果结束：

- `submit` -- task 已完成。
- `block` -- 需要输入或外部依赖。
- `fail` -- 无法完成工作。

任务运行中可以记录 progress。Worker 如果没有终止结果就退出，会被视为协议失败，并可能在重试预算内重试。
