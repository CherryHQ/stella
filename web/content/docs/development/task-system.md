---
title: Task system
description: Durable task execution with computed readiness, run attempts, blockers, reviews, and audit events.
---

The task system is Stella's durable execution layer for work that should outlive one chat turn.

This page describes the implementation contract. User-facing task behavior is documented in [Task System Overview](/docs/task-system/overview).

## Current support matrix

| Area                                           | Status                                                                                 |
| ---------------------------------------------- | -------------------------------------------------------------------------------------- |
| Worker task execution                          | Supported. Worker runs use an executor boundary and `task_control` terminal recording. |
| Task review `none`                             | Supported. Submit completes the task immediately.                                      |
| Task review `auto`                             | Supported. Submit records an automatic approval for audit, then completes.             |
| Task review `human`                            | Supported through review APIs.                                                         |
| Task review `agent`                            | Rejected by the API. Agent-reviewer runtime is not part of this release.               |
| Goal as container with `review_policy=none`    | Supported. Goal status rolls up from child tasks.                                      |
| Goal auto-planning                             | Not supported. Child tasks must be created explicitly.                                 |
| Goal final synthesis / goal review             | Rejected by the API. Goals use `review_policy=none` in this release.                   |
| Planner / synthesizer / reviewer run execution | Not supported. Their dispatcher scan paths are removed in this release.                |

## Persistence model

Durable state is stored in SQLite-backed tables, not in the runtime process.

| Table                      | Purpose                                                                                                                      |
| -------------------------- | ---------------------------------------------------------------------------------------------------------------------------- |
| `agent_task`               | One executable unit. Stores business status, goal link, review policy, context, output, retry counters, and active pointers. |
| `agent_task_run`           | One execution attempt. Stores run kind, attempt number, executor, session, lease, heartbeat, result, and error.              |
| `agent_task_event`         | Append-only audit log for transitions and protocol errors.                                                                   |
| `agent_task_dep`           | DAG edges between tasks, including hard/soft semantics and failure policy.                                                   |
| `agent_task_blocker`       | Why a task is paused. At most one open blocker per task.                                                                     |
| `agent_review`             | Human/auto review records for tasks and goals.                                                                               |
| `agent_goal`               | Container for related tasks; status rolls up from children in the supported mode.                                            |
| `agent_task_dispatch_hint` | One-shot hint that tells the dispatcher which executor agent to use for the next claim.                                      |

Runtime state is intentionally small and temporary. During one run, the worker executor records only the terminal action the agent declared (`submit`, `block`, or `fail`) plus its payload. The worker then applies that result through `TransitionService`.

## State authority

`internal/tasks/transition.go` is the authority for durable state changes. Code outside the transition service should not update task, run, blocker, review, or goal lifecycle columns directly.

Typical write path:

```text
agent/tool loop
  -> worker executor records terminal result
  -> Worker.applyResult
  -> TransitionService.Submit / Block / Fail
  -> SQL tables + agent_task_event
```

Progress is the exception: the task-control progress action may persist a shallow merge into `agent_task.context` during execution. Progress is not terminal and does not change task/run lifecycle state.

## Task lifecycle

```text
draft --activate--> ready --claim--> running --submit--> done or reviewing
  |                    |              |
  |                    |              +-- block --> blocked --resolve/waive--> ready
  |                    |              +-- fail  --> ready (retry) or failed
  +-- cancel --> cancelled
```

`reviewing` is entered only when a supported review policy requires a decision. `human` reviews wait for API/CLI decisions. `auto` is decided immediately. `agent` review is not a supported runtime path yet.

## Readiness, not status

`status='ready'` does not mean dispatchable. Dispatchability is computed from:

- `not_before` scheduling.
- hard and soft dependency state.
- dependency failure policy and waivers.
- active run constraints.
- worker concurrency.
- executor resolution.

`internal/tasks/readiness.go` exposes `Compute(task, deps, now) Readiness`. The dispatcher uses it after a coarse SQL candidate scan.

## Dependencies

An edge can be `hard` or `soft`. `on_failure` is `block`, `fail`, or `ignore`.

| Edge kind | Upstream               | Failure policy | Result                                             |
| --------- | ---------------------- | -------------- | -------------------------------------------------- |
| hard      | `done`                 | any            | satisfied                                          |
| hard      | `failed` / `cancelled` | `ignore`       | satisfied                                          |
| hard      | `failed` / `cancelled` | `block`        | downstream blocks with `dep_failure` unless waived |
| hard      | `failed` / `cancelled` | `fail`         | downstream fails                                   |
| soft      | any terminal state     | ignored        | satisfied                                          |
| any       | non-terminal           | any            | waiting                                            |

A `dep_failure` blocker is resolved by waiving the edge, not by generic blocker resolution. The waiver is attributable and stored on the dependency edge.

## Dispatcher

`internal/tasks.Dispatcher` is the scheduler-facing loop. It is registered by `cmd/stella` as an in-memory recurring task on the scheduler service; it does not create a user-visible scheduler job.

Worker-side tick order:

1. Interrupt stale queued/running runs whose lease expired.
2. Propagate hard dependency failures to downstream tasks.
3. Roll up goals from child task state.
4. Scan ready task candidates.
5. Compute readiness.
6. Resolve an executor.
7. Mint or reuse a task session.
8. Claim the task, creating an `agent_task_run` row.
9. Spawn a `Worker` for that run.

Planner, synthesizer, and agent-reviewer scan paths are not part of the supported runtime and are removed rather than guarded by unsupported events.

## Executor resolution

When claiming a worker task, the dispatcher resolves the executor in this order:

1. Live dispatch hint for `(task_id, kind='worker')`.
2. Existing task session owner, when `task.session_id` is set.
3. Task creator agent (`agent_task.agent_id`).
4. Reject by writing an event and leaving the task unclaimed.

The system must not silently choose a default agent. If no executor can be resolved, the task is misconfigured.

## Session continuity

Each run records the session it used in `agent_task_run.session_id`. The task row also stores `session_id` as the default session for the next worker run.

```text
if task.session_id exists:
    reuse it for the next worker run
else:
    mint a new task session and store it on the task
```

Clear `task.session_id` only when you intentionally want a fresh worker conversation on the next run.

## Worker executor runtime

Worker execution is implemented around the `Executor` interface in `internal/tasks/executor.go`.

Important pieces:

- `Executor.Execute(ctx, Request) (Result, error)` owns agent interaction for one claimed run.
- `workerExecutor` resolves the run's executor agent to a runner factory and injects `task_control`.
- `terminalRecorder` records the first terminal action only.
- `recordingControlTool` is the agent-facing `task_control` tool.
- `Worker.applyResult` applies the single terminal result through `TransitionService`.

Terminal actions:

| Action     | Runtime behavior                                                                 | Durable transition          |
| ---------- | -------------------------------------------------------------------------------- | --------------------------- |
| `progress` | Persist shallow patch to `agent_task.context`; repeatable until terminal action. | No lifecycle transition.    |
| `submit`   | Record output payload.                                                           | `TransitionService.Submit`. |
| `block`    | Record blocker kind/question/detail.                                             | `TransitionService.Block`.  |
| `fail`     | Record reason/retryable.                                                         | `TransitionService.Fail`.   |

The agent-facing tool does not directly complete, block, or fail the task. It declares the outcome; the worker applies it once.

## Protocol repair and failure

If the first worker turn ends without a terminal action, the executor distinguishes two cases:

- **Silent exit** — no assistant text and no terminal action. The worker treats this as a protocol failure immediately.
- **Text-only exit** — assistant text was produced but no terminal action was recorded. The executor runs exactly one repair turn in the same task session. The repair prompt includes the prior text as context and requires one terminal `task_control` action.

If the repair turn records `submit`, `block`, or `fail`, the worker applies that result normally. The runtime never auto-submits free text.

If the repair turn also ends without a terminal action, the executor returns `TerminalNone` with `RepairAttempted=true`. The worker then applies the protocol-error fallback:

- `TransitionService.Fail(... retryable=true)`.
- `agent_task_event` gets `event_type='protocol_error'` with `detail.repair_attempted`.
- The task returns to `ready` if retry budget remains, otherwise `failed`.

## Heartbeat and leases

Active runs carry `lease_expires_at` and `heartbeat_at`. The worker heartbeat extends the lease while the executor runs. If Stella crashes or a worker hangs, the dispatcher eventually marks the stale run interrupted and returns the task to `ready` if retry budget allows.

## API surface

Task routes are flat under `/api/tasks` and scoped by authenticated user context.

| Method         | Path                                                 | Purpose                                                                                                               |
| -------------- | ---------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------- |
| `POST`         | `/api/tasks`                                         | Create a task.                                                                                                        |
| `GET`          | `/api/tasks`                                         | List tasks, optionally filtered by agent/status.                                                                      |
| `GET`          | `/api/tasks/{id}`                                    | Fetch a task.                                                                                                         |
| `POST`         | `/api/tasks/{id}/cancel`                             | Cancel a task.                                                                                                        |
| `POST`         | `/api/tasks/{id}/reopen`                             | Reopen done/failed task.                                                                                              |
| `GET`          | `/api/tasks/{id}/readiness`                          | Explain dispatchability.                                                                                              |
| `GET`          | `/api/tasks/{id}/events`                             | Audit events.                                                                                                         |
| `GET`          | `/api/tasks/{id}/runs`                               | Run attempts.                                                                                                         |
| `GET` / `POST` | `/api/tasks/{id}/deps`                               | List/add dependency edges.                                                                                            |
| `POST`         | `/api/tasks/{id}/deps/{depTaskID}/waive`             | Waive failed hard dependency.                                                                                         |
| `POST`         | `/api/tasks/{id}/blockers/{blockerID}/resolve`       | Resolve a blocker.                                                                                                    |
| `GET`          | `/api/tasks/{id}/reviews`                            | List reviews.                                                                                                         |
| `POST`         | `/api/tasks/{id}/reviews/{reviewID}/approve`         | Approve a review.                                                                                                     |
| `POST`         | `/api/tasks/{id}/reviews/{reviewID}/reject`          | Reject a review.                                                                                                      |
| `POST`         | `/api/tasks/{id}/reviews/{reviewID}/request-changes` | Request changes.                                                                                                      |
| `POST`         | `/api/tasks/{id}/reviews/{reviewID}/escalate`        | Escalate an agent review to human; endpoint exists for schema compatibility, but new agent-review tasks are rejected. |

Any HTTP behavior change must follow the spec-first workflow: update OpenAPI, regenerate generated code, then implement.
