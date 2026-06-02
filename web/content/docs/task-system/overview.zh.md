---
title: 任务系统概览
---

Stella 的任务系统用于处理需要被追踪执行的 goal，而不是只需要一条聊天回答的请求。

**Task** 是一个可执行的工作单元。**Goal** 是一组相关 task 的容器，用来展示整体结果。你需要显式创建任务图：先创建 goal，再用 `--goal-id` 创建子 task，添加依赖，然后激活这些工作。

## Stella 现在能做什么

Stella 可以：

- 在后台执行已激活的 task。
- 在重启后保留 task、run、event、blocker、review 和 goal 状态。
- 在 task 重试时复用任务 session。
- 用 readiness 信息解释一个 task 为什么还没运行。
- 在需要用户输入或外部依赖时阻塞 task。
- 对临时失败进行重试，直到重试预算耗尽。
- 通过 `none`、`auto` 或 `human` review policy 处理 task 输出。
- 在 `review_policy` 为 `none` 时，把 goal 状态从子 task 汇总出来。

Stella 在本版本**不会**自动把 goal 规划拆分成 tasks，并会拒绝 Agent review 和 goal 最终综合结果这类 runtime。需要判断和审批的工作请使用人工 review。

## 从 goal 到 tasks

先写清楚结果：

> 准备 Q2 报销审计包，并标出需要财务 review 的问题。

然后显式创建 tasks：

- 收集报销记录。
- 提取票据信息。
- 根据制度检查每条报销。
- 标记例外。
- 准备 review packet。
- 请财务 review 例外项。

Goal 提供一个统一位置，让你看到整个目标是完成、阻塞、失败，还是仍在推进。

## 依赖关系

依赖关系让执行顺序可见。制度检查没完成前，review packet 不应该开始执行。

适合添加依赖的场景：

- 一个 task 需要另一个 task 的输出。
- 上游 task 失败时，下游 task 应该停止。
- 你希望 readiness 视图准确解释还有什么在等待。

## Review 和审批

有些工作应该停下来等待人类判断。适合人工 review 的场景：

- 制度例外。
- 候选人推荐。
- 发布审批。
- 面向客户的回复。
- 任何会改变金钱、权限或声誉的事情。

`auto` review 会写入一条自动批准记录用于审计。`none` 会在 worker 提交输出后直接完成。本版本 API 会拒绝 agent review。

## 任务 UI

任务 UI 存在的原因很简单：聊天记录不是项目管理工具。用它查看：

- 当前 task 状态。
- 依赖关系和 readiness。
- Blockers。
- 事件和运行记录。
- Review 状态。
- Goal 的子任务和汇总状态。

实用规则：用聊天描述 goal 和决策；用 task 追踪执行。
