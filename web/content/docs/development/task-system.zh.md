---
title: 任务系统
description: 异步、可持久、按 DAG 调度的任务执行;状态描述生命周期,可调度性由代码计算。
---

任务系统承载长时间运行的 agent 工作。**任务**是最小可执行单元,任务之
间组成 DAG。每次执行尝试记录为一条 **run**;暂停记录为 **blocker**;每次
状态变更写一条不可变的 **event**。任务的 `status` 只描述业务生命周期,
"现在能不能跑"是计算出来的视图。

> 设计 issue:[CherryHQ/stella#226](https://github.com/CherryHQ/stella/issues/226)。

## 心智模型

| 概念                       | 用途                                                        |
| -------------------------- | ----------------------------------------------------------- |
| `agent_task`               | 可执行单元,状态严格按生命周期流转                           |
| `agent_task_dep`           | DAG 边。每条边是 `hard`(默认)或 `soft`,带 `on_failure` 策略 |
| `agent_task_run`           | 一次执行尝试。携带 session、heartbeat、lease                |
| `agent_task_blocker`       | 记录任务为何暂停。每个任务最多一条 open blocker             |
| `agent_task_dispatch_hint` | 在创建任务与首次 claim 之间持久化"用哪个 executor agent"    |
| `agent_task_event`         | 仅追加的审计日志,每次状态变更写一行                         |

## 任务生命周期

```
draft ──activate──▶ ready ──claim──▶ running ──submit──▶ done
  │                    │               │
  │                    │               ├─ block ───▶ blocked ──resolve──▶ ready
  │                    │               └─ fail ────▶ ready(retry)或 failed
  └─ cancel ──▶ cancelled
```

任何对 `status` 列的写操作只发生在 `internal/tasks/transition.go`,其
他代码路径(worker、dispatcher、handler)都通过转换服务进入。CI grep
守卫保证这条规则不漂移。

## 可调度性,不是 status

`status='ready'` 不代表能立刻跑。当前能否调度取决于:

- `not_before <= now`(否则 deferred)
- 所有硬依赖已满足(上游 `done`,或上游已终止且有 waiver / `on_failure=ignore`)
- 所有软依赖已终止(`done` / `failed` / `cancelled` 任一)
- 没有 active run 占着这个任务
- 所属 org 下未超并发上限
- executor 能解析出来

`internal/tasks/readiness.go` 暴露一个**纯函数** `Compute(task, deps,
now) Readiness`,返回 `dispatchable` / `waiting_deps` / `deferred` /
`throttled` / `blocked` / `terminal` 等之一。dispatcher 配合 SQL 粗过
滤 `ListReadyCandidates` 使用 —— SQL 端不单独决定可调度性。

## 依赖语义

边可以是 `hard` 或 `soft`,`on_failure` 是 `block`(默认)、`fail` 或
`ignore`。组合结果:

| 边类型 | 上游                 | `on_failure` | 已 waive? | 结果                            |
| ------ | -------------------- | ------------ | --------- | ------------------------------- |
| hard   | `done`               | —            | —         | 满足                            |
| hard   | `failed`/`cancelled` | `ignore`     | —         | 满足                            |
| hard   | `failed`/`cancelled` | `block`      | 否        | 下游 → `blocked`(`dep_failure`) |
| hard   | `failed`/`cancelled` | `block`      | 是        | 满足                            |
| hard   | `failed`/`cancelled` | `fail`       | —         | 下游 → `failed`                 |
| soft   | 任一终止状态         | (忽略)       | —         | 满足                            |
| 任意   | 尚未终止             | —            | —         | 等待                            |

`dep_failure` blocker 不能通过通用的 `ResolveBlocker` 解决 —— 必须走
**显式 waiver**(`WaiveDep`)。waiver 在边上写入 `waived_at` +
`waived_by_user` + 自由文本 reason。少了 waiver,即便 blocker 标记
resolved,readiness 仍然看到失败的上游,task 仍然卡住。

### Waiver 工作流(hard / failed / block)

上游失败、下游边是 `hard` + `on_failure='block'` 时,下一次 dispatcher tick:

1. 计算可调度性 → 命中 `dep_failed_block`。
2. 调 `TransitionService.Block`,kind=`dep_failure`,把下游转到 `blocked`,
   开一条 `agent_task_blocker`。

解锁路径:操作员调 `WaiveDep(taskID, depTaskID, userID, reason)`:

1. 在边上写 `waived_at` + `waived_by_user` + `waiver_reason`。
2. 同事务里把开着的 `dep_failure` blocker 标为 resolved(resolution_json 里
   记录 waiver),清掉 `active_blocker_id`,task 回到 `ready`。
3. 下次 dispatcher tick 重新算 readiness,被 waive 的边视为满足,任务可
   以分派。

软依赖永远不进入 waiver 流程 —— 软依赖忽略 `on_failure`,上游进入任一终
止状态(`done` / `failed` / `cancelled`)立刻视为满足。

## Executor 解析

dispatcher 不在任务行上存"被指派的 agent"。claim 任务时按下面顺序解析
executor,取第一个命中:

1. **dispatch hint** —— `agent_task_dispatch_hint` 中 `(task_id, kind)`
   匹配且 `consumed_at IS NULL` 的行。创建任务时显式指定
   `executor_agent_id` 会写入这张表。claim 同事务里标记 consumed。
2. **session 派生** —— `task.session_id` 非空时,使用拥有这个 session
   的 agent。这是 retry 路径:让重试落在第一次运行的同一个 agent 上。
3. **创建者兜底** —— `task.agent_id`,即任务的创建 agent。
4. **拒绝** —— 三个都没有,dispatcher 写 `protocol_error` 事件并保持
   任务在 `ready`。**不**会静默挑一个 default。

## Session 续接

每条 `agent_task_run` 都记录它跑在哪个 session 上(`session_id`,NOT
NULL)。任务行额外缓存一个 `session_id` 作为"下次 worker run 默认用这
个 session"的指针。dispatcher 的规则:

```
if task.session_id 非空:
    run.session_id := task.session_id
else:
    run.session_id := newSession()
    task.session_id := run.session_id
```

想让 retry 用全新 session(比如对话被污染了),把 `task.session_id` 清
空再重新调度即可。**没有** mode 列 —— null 即新,非 null 即复用。

## Worker 契约

worker 拿到 `RunnerFunc` 和 `TaskControlTool` 后,必须**恰好**调用其
中一个:

- `tool.Progress(patch)` —— 对 `task.context` 做浅 JSON merge,可多次调用,**不**终结。
- `tool.Submit(output)` —— task → `done`,run → `completed`。
- `tool.Block(kind, question, detail)` —— task → `blocked`,run → `cancelled`。
- `tool.Fail(reason, retryable)` —— run → `failed`;有重试预算 task → `ready`,否则 → `failed`。

如果 runner 返回时没有调用过任何一个终结动作,worker 应用
**protocol-error 回退**:run 标记 `failed`,写一条
`event_type='protocol_error'` 事件,有重试预算就让 task 回到 `ready`。

Runner panic 转为不可重试的 `Fail`。

## Heartbeat 与 lease

活跃 run 携带 `lease_expires_at`(默认 90 秒)和 `heartbeat_at`。worker
的 heartbeat goroutine 每 20 秒续约一次。dispatcher 的 stale-run sweep
扫描 `status IN ('queued','running') AND lease_expires_at < now` 的行,
标记 `interrupted`,有重试预算就把任务返回 `ready`。

这就是 worker 崩溃或进程重启后的恢复路径:lease 到期 → 下次 tick 重新
认领。

## DB 强约束

下面这些是 partial unique index 或 CHECK,不靠应用层自律:

- `uniq_active_worker_run` —— 每个任务最多一条 queued/running 的 worker run
- `uniq_task_run_attempt` —— `(task_id, kind, attempt_no)` 唯一
- `uniq_open_blocker_per_task` —— 每个任务最多一条 open blocker
- `uniq_active_dispatch_hint_task` —— 每个 `(task_id, kind)` 最多一条未消费 hint
- `agent_task.active_run_id` / `active_blocker_id` 用 `ON DELETE RESTRICT`,
  转换服务必须先清指针再删子记录

## Boot 装配

`tasks.New(BootConfig{...})` 返回 `*tasks.Service`,内含 queries、
transition service、facade、dispatcher。cmd/stella 在 boot 时构造一个,
并在现有 `scheduler.Service` 上把 `Dispatcher.Tick` 注册成内存 recurring
task(不写 `sched_job` 行)。`Dispatcher.Stop` 在停服时等 worker 排空。

## 当前进度

Slice 1(MVP)已落地:

- schema、转换服务、可调度性计算、worker、dispatcher 全套
- 程序级 facade(`tasks.ServiceFacade`)
- Boot 装配;dispatcher tick 已注册到 scheduler

**待办(后续 PR 跟进):**

- 真正的 agent.Pool ↔ RunnerFunc 适配器(目前 runner 是 noop,显式失
  败,以便日志里能看到)
- Reviewer dispatcher — `review_policy='agent'` 当前能正确把任务停在
  `reviewing`,但还没有 reviewer run 被派发。
- Goal 实体(`agent_goal` + planner / synthesizer + rollup)。

## HTTP 接口

所有路由都是扁平的 `/api/tasks/...`,通过 `X-Stella-Org-ID` 请求头(缺
失时回退到会话默认组织)进行组织作用域。跨组织访问返回 404(而不是
403),避免泄露资源存在性。

| 方法 | 路径                                                 | 用途                             |
| ---- | ---------------------------------------------------- | -------------------------------- |
| POST | `/api/tasks`                                         | 创建任务                         |
| GET  | `/api/tasks`                                         | 列表(支持 `agent_id` / `status`) |
| GET  | `/api/tasks/{id}`                                    | 获取单个任务                     |
| POST | `/api/tasks/{id}/cancel`                             | 取消                             |
| POST | `/api/tasks/{id}/reopen`                             | 重开(可带 `cascade`)             |
| GET  | `/api/tasks/{id}/readiness`                          | 调度性视图                       |
| GET  | `/api/tasks/{id}/events`                             | 审计日志                         |
| GET  | `/api/tasks/{id}/runs`                               | 运行尝试列表                     |
| GET  | `/api/tasks/{id}/deps` / POST 同路径                 | 依赖边列表 / 添加                |
| POST | `/api/tasks/{id}/deps/{depTaskID}/waive`             | 豁免硬依赖失败                   |
| POST | `/api/tasks/{id}/blockers/{blockerID}/resolve`       | 解除阻塞                         |
| GET  | `/api/tasks/{id}/reviews`                            | 列出评审                         |
| POST | `/api/tasks/{id}/reviews/{reviewID}/approve`         | 通过                             |
| POST | `/api/tasks/{id}/reviews/{reviewID}/reject`          | 拒绝                             |
| POST | `/api/tasks/{id}/reviews/{reviewID}/request-changes` | 请求修改                         |
| POST | `/api/tasks/{id}/reviews/{reviewID}/escalate`        | 升级 agent 评审到人工            |

典型错误编码:

| 条件                               | HTTP | code                                        |
| ---------------------------------- | ---- | ------------------------------------------- |
| 任务 / blocker / 评审不存在        | 404  | `not_found`                                 |
| 非法状态迁移                       | 409  | `invalid_transition`                        |
| 依赖边会形成环                     | 409  | `dep_cycle`                                 |
| 评审已结束                         | 409  | `review_closed`                             |
| dep_failure 类型 blocker(需要豁免) | 409  | `dep_failure_requires_waiver`               |
| blocker 已关闭                     | 409  | `blocker_already_closed`                    |
| reopen 会使下游孤立                | 409  | `reopen_conflict`(body 含 `downstream_ids`) |
