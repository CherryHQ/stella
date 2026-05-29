---
title: 目标系统
description: 在任务系统之上叠加规划与综合,带 rollup、planner / synthesizer 运行和评审管道。
---

目标系统在任务系统之上叠加规划 + 综合 pass。**Goal** 是 `agent_goal` 中
的一行,拥有一组子任务。dispatcher 通过四种 run 和一个纯 rollup 函数把
goal 从 draft → planning → running → (reviewing) → done 推进。

> 建立在[任务系统](./task-system)之上。

## 心智模型

| 概念                 | 用途                                                           |
| -------------------- | -------------------------------------------------------------- |
| `agent_goal`         | 一组相关任务的容器,有 status / review policy / active review。 |
| `agent_task.goal_id` | 一个任务可以属于一个 goal(或独立)。                            |
| `planner` run        | 生成 goal 的草稿子任务,在 goal 处于 `draft` 时下发。           |
| `synthesizer` run    | 把子任务输出聚合到 `agent_goal.output`,在必需依赖满足后下发。  |
| `reviewer` run       | 与任务侧相同;可评审 goal-parented `agent_review` 行。          |
| `agent_review`       | 每个 parent(task 或 goal,XOR)一条评审行。                      |

## Goal 生命周期

```
draft ── planner ──▶ draft(子任务被创建)
  │
  └─ ActivateGoal ──▶ running ── rollup 说必需子任务全部完成 ─▶ done(policy=none)
                                                          ├▶ reviewing(synthesizer + agent/human 评审)
                                                          └▶ failed(必需子任务失败)
running ── 必需子任务 blocked ──▶ blocked
非终止 ── CancelGoal ──▶ cancelled(级联取消非终止子任务)
```

## Rollup

`internal/tasks/goal_rollup.go` 暴露一个纯函数:

```go
RollupGoal(goal, childCounts, hasOpenSynth) GoalNextState
```

对 `running` 状态的 goal,决策表:

| 必需子任务   | review_policy      | 结论                                       |
| ------------ | ------------------ | ------------------------------------------ |
| 任一 failed  | —                  | NextStatus=failed                          |
| 任一 blocked | —                  | NextStatus=blocked                         |
| 任一 pending | —                  | 无操作                                     |
| 全部 done    | `none`             | NextStatus=done                            |
| 全部 done    | `auto/agent/human` | SpawnSynthesizer=true(除非已有 synth 在跑) |

其他 goal 状态在 rollup 层是 no-op —— 它们的转换在 `ActivateGoal`、评审
决策等位置完成。

## Dispatcher tick

每次 tick(在任务侧步骤之后)新增:

1. `rollupGoals` —— 对每个非终止 goal 运行 `RollupGoal` 并应用结论。
2. `scanAndDispatchReviewers` —— 对每条 reviewer_run_id 未设置的 open agent
   评审(task- 或 goal-parented)创建一条 `reviewer` run,通过
   `SetAgentReviewReviewerRun` 关联。
3. `scanAndDispatchPlanners` —— 对每个 draft goal 创建一条 planner run。
4. `scanAndDispatchSynthesizers` —— 对每个 rollup 说"该 synth"的 running
   goal 创建一条 synthesizer run。

### Noop runner 状态(当前 PR)

reviewer / planner / synthesizer run 会被创建到数据库,然后立刻被
`failGoalRunAsNoop` / `failReviewerRunAsNoop` 标 failed,并写一条
`protocol_error` 事件。这样在没接 `agent.Pool` 适配器前,dispatch 路径
就已经在 events / runs 里可观察。后续 PR 会用真实执行替换 immediate-fail。

实际表现:

- `review_policy='agent'` 的评审会拿到 reviewer run + 进入 in_progress,
  然后 run 失败(在事件里看到 `dispatch_reviewer` + `protocol_error`)。
- draft goal 每次 tick 会派一条 planner run(每条都失败);unique-active
  partial index 保证同一 tick 内不会重复。
- 通过 `event_type='protocol_error' AND detail->>'reason' LIKE 'noop_%'`
  可以快速定位"还没接真 runner"的 goal。

## Goal 评审决策

`ApproveReview` / `RejectReview` / `RequestChanges` 按 parent 类型分发
(`internal/tasks/review.go:decideAnyReview`):

- task parent:原有行为(approve→done,reject→failed,
  request_changes→ready 或按 retry 预算判 failed)。
- goal parent:approve→`goal.done`,reject→`goal.failed`,
  request_changes→`goal.failed`(已知缺口 —— goal 侧暂无重试预算,留待后
  续)。

`EscalateReview` 会把对应的 `active_review_id`(task 或 goal)指向新建
的 human 评审行。

## HTTP 接口

| 方法 | 路径                                                                              | 用途                                          |
| ---- | --------------------------------------------------------------------------------- | --------------------------------------------- |
| POST | `/api/goals`                                                                      | 创建 goal(draft)。                            |
| GET  | `/api/goals`                                                                      | 列出当前组织的 goal。                         |
| GET  | `/api/goals/{id}`                                                                 | 获取单个 goal。                               |
| POST | `/api/goals/{id}/activate`                                                        | Draft → running;把 draft 子任务提升到 ready。 |
| POST | `/api/goals/{id}/cancel`                                                          | 级联取消非终止子任务。                        |
| GET  | `/api/goals/{id}/tasks`                                                           | 列出子任务。                                  |
| GET  | `/api/goals/{id}/reviews`                                                         | 列出 goal 评审。                              |
| POST | `/api/goals/{id}/reviews/{reviewID}/approve`(reject / request-changes / escalate) | 对 goal 评审作决定。                          |

访问通过认证会话进行作用域控制。

## 待办

- 真正的 `agent.Pool` ↔ runner 适配器,覆盖 reviewer / planner / synthesizer。
  在此之前所有三条 dispatch 路径都会 `protocol_error`。
- Goal 侧 `request_changes` → synthesizer 重试预算(目前直接判 failed)。
- Goal 的 Web UI。
