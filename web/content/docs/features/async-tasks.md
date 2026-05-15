---
title: Async Tasks
---

## Status

Implemented — `internal/tasks/` package with SQLite persistence, per-user concurrency limits, DAG dependency tracking, human-in-the-loop review flows, and a built-in `task_control` tool injected into each task session.

## Overview

Async tasks let agents (and users) queue long-running work that executes independently of the current conversation. Unlike the synchronous `agent` tool — which blocks until the subtask completes — an async task is fire-and-forget: it runs in the background, checkpoints its state to the database, pauses when it needs human input, and survives process restarts.

Typical use cases:

- Research or refactoring tasks that take minutes or hours
- Multi-step pipelines where a human must approve each stage
- Delegated subtasks that the main agent should not wait on

## Architecture

```
User / Agent (via task tool or API)
    |
    |  create task
    v
Task Service  ----writes---->  agent_tasks (SQLite)
                                    |
                         Internal Scheduler tick (30s)
                                    |
                 +------------------+------------------+
                 |                  |                  |
          notify sweep       dep failure check    dispatch pending
                 |                                     |
         notify_at <= now                    per-user limit + deps done
                 |                                     |
         send notification                      Worker goroutine
         notify_at = NULL                             |
                                              task_control tool
                                                     |
                                          status transitions + notify_at
```

### Package: `internal/tasks/`

| File                             | Purpose                                                                 |
| -------------------------------- | ----------------------------------------------------------------------- |
| `internal/tasks/service.go`      | `Service` — lifecycle, CRUD, action handling, dispatch                  |
| `internal/tasks/worker.go`       | Worker goroutine — claims task, runs agent loop, persists history       |
| `internal/tasks/control_tool.go` | `task_control` tool — injected into task sessions for state transitions |
| `plugins/tools/task/task.go`     | `task` tool — available to all agents for creating and querying tasks   |

## Task Lifecycle

### Status machine

```
pending → running → done
                 → failed
                 → blocked      → pending  (user responds)
                 → review_requested → pending  (user approves)
                                   → failed   (user rejects)
any non-terminal → cancelled  (user cancels)
```

| Status             | Meaning                                          |
| ------------------ | ------------------------------------------------ |
| `pending`          | Queued, waiting for scheduler to dispatch        |
| `running`          | Worker goroutine is executing the agent loop     |
| `blocked`          | Agent needs human input before continuing        |
| `review_requested` | Agent completed a phase and needs human approval |
| `done`             | Completed successfully                           |
| `failed`           | Terminated with an error or rejected by user     |
| `cancelled`        | Cancelled by user action                         |

### Event log

Every status transition appends a row to `agent_task_events` with an `event_type` (`started`, `progress`, `blocked`, `review_requested`, `done`, `failed`, `cancelled`) and a human-readable `detail`. The event log is append-only and serves as the audit trail visible in the task detail UI.

## DAG Dependencies

Tasks can declare dependencies on other tasks via a `deps` JSON array field on the task row. The scheduler only dispatches a task when all its dependencies are in the `done` state.

If any dependency reaches `failed` or `cancelled`, the dependent task transitions to `blocked` and `notify_at` is set so the user is informed. The dependent task is not auto-failed — the user decides how to proceed.

**Circular dependency prevention:** the creation path (API and `task` tool) walks the dependency graph before inserting and rejects cycles with an error.

## Scheduler Integration

The task scheduler runs as an internal gocron job registered via `scheduler.ScheduleEvery`. It does not appear in the scheduler UI or API. Each tick (every 30 seconds) performs three sweeps in order:

1. **Notification sweep** — query `WHERE notify_at IS NOT NULL AND notify_at <= now`, send notifications, then set `notify_at = NULL`.
2. **Dependency failure check** — for each `pending` task whose deps include a `failed` or `cancelled` task, transition to `blocked` and set `notify_at = now`.
3. **Dispatch** — for each `pending` task whose deps are all `done` and whose owner is below the per-user concurrency limit, claim the task (`pending → running`) and launch a worker goroutine.

The concurrency limit is counted from the database (`SELECT count(*) WHERE status='running' AND user_id=?`), not from an in-memory semaphore, so it is accurate across restarts.

### Crash recovery

On `Service.Start`, any task in `running` status is reset to `pending`. The next scheduler tick re-dispatches it. The worker reconstructs conversation history from the memory provider using the stable session key `task:{task_id}`.

## Worker Goroutine

Each dispatched task runs in its own goroutine. The worker:

1. Atomically claims the task (`pending → running`) — guards against duplicate dispatch.
2. Logs a `started` event.
3. Builds a `pkg/agent.Runner` with the full tool registry **plus** the `task_control` tool injected for this session only.
4. Bootstraps the memory session (`task:{task_id}`) and assembles prior conversation history.
5. On first run: sends the task title and description as the initial user message.
6. On resume: replays conversation history and appends a `[Resume]` prompt with the task ID.
7. Runs the agent loop (`runner.Run`).
8. Persists new messages back to the memory provider.
9. If the agent exits without calling `task_control`, the worker auto-marks the task `done` (clean exit) or `failed` (error).

Each worker holds a `context.CancelFunc`. The service cancels it when the user cancels the task or the process shuts down.

## task_control Tool

`task_control` is injected exclusively into task worker sessions. It is the agent's only mechanism for signalling state transitions. It is never available in normal conversation sessions.

### Actions

| Action           | Effect                                                                                                                                                              |
| ---------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `progress`       | Updates the `context` JSON field (current phase, decisions, metadata). Optionally sets `notify_at` for a deferred user reminder. Does **not** stop the runner.      |
| `block`          | Sets `status = blocked`, writes `notify_at = now`, cancels the runner. Task waits for user `respond`.                                                               |
| `request_review` | Writes structured `review_request` JSON, sets `status = review_requested`, writes `notify_at = now`, cancels the runner. Task waits for user `approve` or `reject`. |
| `done`           | Stores `message` in `context.output`, sets `status = done`, cancels the runner.                                                                                     |
| `failed`         | Sets `status = failed`, cancels the runner.                                                                                                                         |

The `block` and `request_review` actions cancel the runner context immediately so the agent stops generating. Notification is deferred to the scheduler tick (≤30s latency), keeping the control tool free of direct notifier dependencies.

### Input schema

```json
{
  "action": "progress | block | request_review | done | failed",
  "message": "human-readable description (required for block, request_review, done, failed)",
  "context": { "...": "optional metadata for progress" },
  "review_request": {
    "question": "...",
    "options": ["..."],
    "recommendation": "...",
    "risk": "low | medium | high",
    "details": "..."
  },
  "notify_after": "2h  (optional, for progress — sets notify_at = now + interval)"
}
```

## task Tool

The `task` tool is available to all agents (not just task sessions). It lets the main agent delegate work or check on running tasks.

| Action   | Description                                                                  |
| -------- | ---------------------------------------------------------------------------- |
| `create` | Create a new async task with title, description, priority, and optional deps |
| `get`    | Retrieve a task's current status and context by ID                           |
| `list`   | List tasks, optionally filtered by status                                    |

## Working with `agent` and `scheduler`

Async tasks complement the existing execution tools:

| Tool        | Use when                                                | Behavior                                        |
| ----------- | ------------------------------------------------------- | ----------------------------------------------- |
| `agent`     | A focused helper is needed and the answer is needed now | Runs synchronously and returns inline           |
| `task`      | Work should continue in the background                  | Persists state, can pause/review, resumes later |
| `scheduler` | Work should start later or repeat                       | Triggers an agent prompt on a clock             |

Common combinations:

- **Agent → task:** the main agent creates a task for long-running work, then returns the task ID to the user.
- **Task → agent:** a task worker can use the synchronous `agent` tool for short focused subtasks, then aggregate the results and call `task_control`.
- **Scheduler → task:** a scheduled job can create a task when recurring work may run long or need human review.

## Human-in-the-Loop Flows

### Blocked (agent needs information)

```
agent calls task_control(block, message="Which branch should I target?")
  → status = blocked, notify_at = now
  → scheduler tick sends notification to user
  → user calls POST /api/tasks/{id}/action  { "action": "respond", "message": "use main" }
  → response appended to memory session
  → status = pending
  → scheduler dispatches worker, agent resumes with user's answer in history
```

### Review requested (agent needs approval)

```
agent calls task_control(request_review, review_request={...})
  → status = review_requested, review_request JSON stored, notify_at = now
  → user calls POST /api/tasks/{id}/action  { "action": "approve" }
  → review_request cleared, status = pending, re-dispatched
  OR
  → user calls POST /api/tasks/{id}/action  { "action": "reject", "message": "reason" }
  → status = failed, rejected event logged
```

## Notifications

Notifications use a single `notify_at` field on the task row:

- `NULL` — nothing pending
- Timestamp — scheduler will notify at or after this time

After sending, the scheduler sets `notify_at = NULL`. Notification content is derived from the task's current status and the most recent event detail. The event log retains full history.

## API

| Method   | Endpoint                 | Description                                              |
| -------- | ------------------------ | -------------------------------------------------------- |
| `GET`    | `/api/tasks`             | List tasks (non-admin: own tasks only)                   |
| `POST`   | `/api/tasks`             | Create a task                                            |
| `GET`    | `/api/tasks/{id}`        | Get task detail                                          |
| `PUT`    | `/api/tasks/{id}`        | Update title, description, priority, agent_id            |
| `DELETE` | `/api/tasks/{id}`        | Delete task (cancels running worker)                     |
| `POST`   | `/api/tasks/{id}/action` | Perform action: `approve`, `reject`, `respond`, `cancel` |
| `GET`    | `/api/tasks/{id}/events` | List task events (chronological)                         |

Ownership is enforced at the handler layer. Non-admin users can only access their own tasks. Admin users can access all tasks.

## Configuration

The per-user concurrency limit defaults to 5 and is set via `tasks.Config.MaxConcurrency` at service construction. There is no runtime UI for this setting; change it in the server wiring if needed.
