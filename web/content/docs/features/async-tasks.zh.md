---
title: 异步任务
---

## 状态

已实现 — `internal/tasks/` 包，支持 SQLite 持久化、按用户并发限制、DAG 依赖追踪、人机协作审核流程，以及注入每个任务会话的内置 `task_control` 工具。

## 概述

异步任务让 agent（和用户）可以将耗时工作排队，独立于当前对话在后台执行。与同步的 `agent` 工具（阻塞等待子任务完成）不同，异步任务是即发即忘的：在后台运行，将状态存入数据库，需要人工介入时暂停，进程重启后可恢复。

典型使用场景：

- 需要数分钟到数小时的研究或重构任务
- 每个阶段都需要人工审批的多步骤流水线
- 主 agent 无需等待的委托子任务

## 架构

```
用户 / Agent（通过 task 工具或 API）
    |
    |  创建任务
    v
Task Service  ----写入---->  agent_tasks（SQLite）
                                    |
                         内部调度器 tick（30 秒）
                                    |
                 +------------------+------------------+
                 |                  |                  |
          通知扫描            依赖失败检测           派发 pending 任务
                 |                                     |
         notify_at <= now                    按用户并发限制 + 依赖满足
                 |                                     |
         发送通知                               Worker goroutine
         notify_at = NULL                             |
                                              task_control 工具
                                                     |
                                          状态转换 + notify_at 写入
```

### 包：`internal/tasks/`

| 文件                             | 用途                                                         |
| -------------------------------- | ------------------------------------------------------------ |
| `internal/tasks/service.go`      | `Service` — 生命周期管理、CRUD、动作处理、任务派发           |
| `internal/tasks/worker.go`       | Worker goroutine — 认领任务、运行 agent 循环、持久化对话历史 |
| `internal/tasks/control_tool.go` | `task_control` 工具 — 注入任务会话，用于状态转换             |
| `plugins/tools/task/task.go`     | `task` 工具 — 所有 agent 可用，用于创建和查询任务            |

## 任务生命周期

### 状态机

```
pending → running → done
                 → failed
                 → blocked          → pending（用户 respond）
                 → review_requested → pending（用户 approve）
                                    → failed（用户 reject）
任意非终态 → cancelled（用户主动取消）
```

| 状态               | 含义                                   |
| ------------------ | -------------------------------------- |
| `pending`          | 已排队，等待调度器派发                 |
| `running`          | Worker goroutine 正在执行 agent 循环   |
| `blocked`          | Agent 需要人工输入才能继续             |
| `review_requested` | Agent 完成某阶段，需要人工审批才能继续 |
| `done`             | 成功完成                               |
| `failed`           | 因错误终止或被用户拒绝                 |
| `cancelled`        | 被用户主动取消                         |

### 事件日志

每次状态转换都会向 `agent_task_events` 表追加一条记录，包含 `event_type`（`started`、`progress`、`blocked`、`review_requested`、`done`、`failed`、`cancelled`）和人可读的 `detail`。事件日志只追加不修改，是任务详情 UI 中显示的审计轨迹。

## DAG 依赖

任务可以通过任务行上的 `deps` JSON 数组字段声明对其他任务的依赖。调度器只会在所有依赖任务状态为 `done` 时才派发该任务。

若任意依赖任务达到 `failed` 或 `cancelled` 状态，被依赖方任务将转为 `blocked`，并设置 `notify_at` 以通知用户。依赖方任务不会自动失败——由用户决定如何处理。

**循环依赖防护：** 创建路径（API 和 `task` 工具）在插入前会遍历依赖图，检测到环则返回错误拒绝创建。

## 调度器集成

任务调度器以内部 gocron job 的形式运行，通过 `scheduler.ScheduleEvery` 注册，不显示在调度器 UI 或 API 中。每次 tick（每 30 秒）按顺序执行三项扫描：

1. **通知扫描** — 查询 `WHERE notify_at IS NOT NULL AND notify_at <= now`，发送通知，然后将 `notify_at` 置为 `NULL`。
2. **依赖失败检测** — 对每个 `pending` 任务，若其依赖中有 `failed` 或 `cancelled` 的任务，则转为 `blocked` 并设置 `notify_at = now`。
3. **派发** — 对每个依赖全部 `done`、且所属用户未超并发限制的 `pending` 任务，原子认领（`pending → running`）并启动 worker goroutine。

并发限制从数据库计数（`SELECT count(*) WHERE status='running' AND user_id=?`），不依赖内存信号量，进程重启后依然准确。

### 崩溃恢复

`Service.Start` 时，所有 `running` 状态的任务会被重置为 `pending`。下一次调度器 tick 会重新派发。Worker 通过稳定的 session key `task:{task_id}` 从 memory provider 重建对话历史。

## Worker Goroutine

每个被派发的任务运行在独立的 goroutine 中。Worker 的执行流程：

1. 原子认领任务（`pending → running`）——防止重复派发。
2. 记录 `started` 事件。
3. 构建 `pkg/agent.Runner`，包含完整工具注册表**加上**仅注入本会话的 `task_control` 工具。
4. 初始化 memory session（`task:{task_id}`）并组装历史对话。
5. 首次运行：将任务标题和描述作为初始用户消息发送。
6. 恢复运行：重放对话历史，追加含任务 ID 的 `[Resume]` 提示。
7. 运行 agent 循环（`runner.Run`）。
8. 将新消息持久化回 memory provider。
9. 若 agent 未调用 `task_control` 就退出，worker 自动将任务标记为 `done`（正常退出）或 `failed`（出错）。

每个 worker 持有一个 `context.CancelFunc`，用户取消任务或进程关闭时由 service 调用。

## task_control 工具

`task_control` 仅注入到任务 worker 会话中，是 agent 发出状态转换信号的唯一机制，在普通对话会话中不可用。

### 动作说明

| 动作             | 效果                                                                                                                                          |
| ---------------- | --------------------------------------------------------------------------------------------------------------------------------------------- |
| `progress`       | 更新 `context` JSON 字段（当前阶段、决策、元数据）。可选设置 `notify_at` 以延迟通知用户。**不**停止 runner。                                  |
| `block`          | 设置 `status = blocked`，写入 `notify_at = now`，取消 runner。任务等待用户 `respond`。                                                        |
| `request_review` | 写入结构化 `review_request` JSON，设置 `status = review_requested`，写入 `notify_at = now`，取消 runner。任务等待用户 `approve` 或 `reject`。 |
| `done`           | 将 `message` 存入 `context.output`，设置 `status = done`，取消 runner。                                                                       |
| `failed`         | 设置 `status = failed`，取消 runner。                                                                                                         |

`block` 和 `request_review` 会立即取消 runner context，使 agent 停止生成。通知延迟至调度器 tick 发送（最多 30 秒延迟），使 control 工具不直接依赖 notifier。

### 输入 Schema

```json
{
  "action": "progress | block | request_review | done | failed",
  "message": "人可读描述（block、request_review、done、failed 时必填）",
  "context": { "...": "progress 时的可选元数据" },
  "review_request": {
    "question": "...",
    "options": ["..."],
    "recommendation": "...",
    "risk": "low | medium | high",
    "details": "..."
  },
  "notify_after": "2h（可选，用于 progress——设置 notify_at = now + 间隔）"
}
```

## task 工具

`task` 工具面向所有 agent（不限于任务会话），让主 agent 可以委派工作或查询任务状态。

| 动作     | 说明                                                         |
| -------- | ------------------------------------------------------------ |
| `create` | 创建新的异步任务，支持标题、描述、优先级和可选的依赖任务列表 |
| `get`    | 通过 ID 查询任务当前状态和 context                           |
| `list`   | 列出任务，可按状态过滤                                       |

## 与 `agent` 和 `scheduler` 配合

异步任务是对现有执行工具的补充：

| 工具        | 适用场景                            | 行为                                  |
| ----------- | ----------------------------------- | ------------------------------------- |
| `agent`     | 需要一个专注 helper，且当前就要结果 | 同步执行，结果直接返回                |
| `task`      | 工作应在后台继续                    | 持久化状态，可暂停/审核，并可稍后恢复 |
| `scheduler` | 工作需要延后开始或周期性重复        | 按时间触发一次 agent prompt           |

常见组合：

- **Agent → task：** 主 agent 为长时间任务创建异步任务，然后把任务 ID 返回给用户。
- **Task → agent：** 任务 worker 可以用同步的 `agent` 工具处理短小、聚焦的子任务，再汇总结果并调用 `task_control`。
- **Scheduler → task：** 定时任务如果可能运行很久或需要人工审核，可以在触发时创建异步任务。

## 人机协作流程

### Blocked（agent 需要信息）

```
agent 调用 task_control(block, message="应该基于哪个分支？")
  → status = blocked，notify_at = now
  → 调度器 tick 向用户发送通知
  → 用户调用 POST /api/tasks/{id}/action  { "action": "respond", "message": "用 main 分支" }
  → 回复追加到 memory session
  → status = pending
  → 调度器派发 worker，agent 恢复执行，对话历史中包含用户回复
```

### 审核请求（agent 需要审批）

```
agent 调用 task_control(request_review, review_request={...})
  → status = review_requested，review_request JSON 存入数据库，notify_at = now
  → 用户调用 POST /api/tasks/{id}/action  { "action": "approve" }
  → review_request 清空，status = pending，重新派发
  或者
  → 用户调用 POST /api/tasks/{id}/action  { "action": "reject", "message": "原因" }
  → status = failed，记录 rejected 事件
```

## 通知机制

通知通过任务行上的单个 `notify_at` 字段管理：

- `NULL` — 无待发通知
- 有时间戳 — 调度器在该时间之后发送通知

发送后调度器将 `notify_at` 置为 `NULL`。通知内容从任务当前状态和最近一条事件详情派生。事件日志保留完整历史。

## API

| 方法     | 路径                     | 说明                                               |
| -------- | ------------------------ | -------------------------------------------------- |
| `GET`    | `/api/tasks`             | 列出任务（非管理员：仅限自己的任务）               |
| `POST`   | `/api/tasks`             | 创建任务                                           |
| `GET`    | `/api/tasks/{id}`        | 获取任务详情                                       |
| `PUT`    | `/api/tasks/{id}`        | 更新标题、描述、优先级、agent_id                   |
| `DELETE` | `/api/tasks/{id}`        | 删除任务（取消运行中的 worker）                    |
| `POST`   | `/api/tasks/{id}/action` | 执行动作：`approve`、`reject`、`respond`、`cancel` |
| `GET`    | `/api/tasks/{id}/events` | 列出任务事件（按时间正序）                         |

所有权在 handler 层强制执行。非管理员用户只能访问自己的任务，管理员可访问所有任务。

## 配置

按用户并发限制默认为 5，通过服务构建时的 `tasks.Config.MaxConcurrency` 设置。该配置无运行时 UI，如需调整请修改服务器 wiring。
