---
title: Task system
description: Async, durable, DAG-scheduled task execution with computed readiness, runs, blockers, and an audit log.
---

The task system runs long-lived agent work. A **task** is the smallest
executable unit. Tasks form a DAG. Each execution attempt is recorded as a
**run**. Pauses are recorded as **blockers**. Every state transition writes
an immutable **event** row. Status describes business lifecycle only;
whether a task is actually dispatchable is a **computed** view.

> Design issue: [CherryHQ/stella#226](https://github.com/CherryHQ/stella/issues/226).

## Mental model

| Concept                    | Purpose                                                                           |
| -------------------------- | --------------------------------------------------------------------------------- |
| `agent_task`               | Executable unit. Has a status that follows a strict lifecycle.                    |
| `agent_task_dep`           | DAG edges. Each edge is `hard` (default) or `soft`, with an `on_failure` policy.  |
| `agent_task_run`           | One execution attempt. Carries the session, heartbeat, lease.                     |
| `agent_task_blocker`       | Records why a task is paused. At most one open blocker per task.                  |
| `agent_task_dispatch_hint` | Persists "use this executor agent" between task creation and the first run claim. |
| `agent_task_event`         | Append-only audit log. Every transition writes one row.                           |

## Task lifecycle

```
draft ──activate──▶ ready ──claim──▶ running ──submit──▶ done
  │                    │               │
  │                    │               ├─ block ───▶ blocked ──resolve──▶ ready
  │                    │               └─ fail ────▶ ready (retry) or failed
  └─ cancel ──▶ cancelled
```

Status writes only happen inside `internal/tasks/transition.go`. Every
other code path — workers, dispatchers, handlers — calls the transition
service. A grep guard catches drift.

## Readiness, not status

`status='ready'` does not mean dispatchable. Whether a task can run _now_
depends on:

- `not_before <= now` (deferred otherwise)
- all hard deps satisfied (upstream `done`, or upstream terminal with a
  waiver / `on_failure=ignore`)
- all soft deps terminal (any of `done`, `failed`, `cancelled`)
- no active run already claiming the task
- concurrency cap below the limit
- an executor can be resolved

`internal/tasks/readiness.go` exposes a **pure function** `Compute(task,
deps, now) Readiness` that returns one of `dispatchable`, `waiting_deps`,
`deferred`, `throttled`, `blocked`, `terminal`, etc. The dispatcher uses
this alongside the coarse `ListReadyCandidates` SQL pre-filter — the SQL
side never decides dispatchability on its own.

## Dependencies

An edge can be `hard` or `soft`. `on_failure` is `block` (default), `fail`,
or `ignore`. Combined:

| Edge kind | Upstream             | `on_failure` | Waived? | Result                                    |
| --------- | -------------------- | ------------ | ------- | ----------------------------------------- |
| hard      | `done`               | —            | —       | satisfied                                 |
| hard      | `failed`/`cancelled` | `ignore`     | —       | satisfied                                 |
| hard      | `failed`/`cancelled` | `block`      | no      | downstream → `blocked` with `dep_failure` |
| hard      | `failed`/`cancelled` | `block`      | yes     | satisfied                                 |
| hard      | `failed`/`cancelled` | `fail`       | —       | downstream → `failed`                     |
| soft      | any terminal         | (ignored)    | —       | satisfied                                 |
| either    | not yet terminal     | —            | —       | waiting                                   |

`dep_failure` blockers cannot be resolved via the generic
`ResolveBlocker` path — they require an attributable **waiver** through
`WaiveDep`. The waiver records `waived_at` + `waived_by_user` + a free-text
reason on the edge itself. Without the waiver, simply marking the blocker
resolved would leave readiness still seeing a failed upstream.

### Waiver workflow (hard / failed / block)

When an upstream task fails and the downstream's edge is `hard` with
`on_failure='block'`, the dispatcher's next tick:

1. Computes readiness → sees `dep_failed_block`.
2. Calls `TransitionService.Block` with `kind=dep_failure`, transitioning the
   downstream to `blocked` with an open `agent_task_blocker`.

To unblock, an operator must call `WaiveDep(taskID, depTaskID, userID, reason)`:

1. Records `waived_at` + `waived_by_user` + `waiver_reason` on the edge.
2. In the same tx, resolves the open `dep_failure` blocker (writes
   `resolution_json` mentioning the waiver), clears
   `agent_task.active_blocker_id`, and transitions the task back to `ready`.
3. The next dispatcher tick re-evaluates readiness; the waived edge is now
   `satisfied`, and the task dispatches.

Soft deps never trigger the waiver flow — `on_failure` is ignored for soft
deps, and a soft dep is satisfied as soon as its upstream is terminal in
any state (`done`, `failed`, or `cancelled`).

## Executor resolution

The dispatcher does not store an "assignee" on the task row. When it
claims a task it resolves the executor in order:

1. **Live dispatch hint** — a row in `agent_task_dispatch_hint` with
   matching `(task_id, kind)` and `consumed_at IS NULL`. Set when a task
   is created with an explicit `executor_agent_id`. Marked consumed in
   the claim transaction.
2. **Session-derived agent** — if `task.session_id` is non-null, use the
   agent that owns that session. This is the retry path: keep running as
   the same agent that started the work.
3. **Creator fallback** — `task.agent_id`, the agent that created the row.
4. **Reject** — if all three are null, the dispatcher writes a
   `protocol_error` event and leaves the task at `ready`. The system
   does not silently pick a default.

## Session continuity

Each `agent_task_run` records the session it ran in (`session_id`, NOT
NULL). The task also caches its current `session_id` as a "default for the
next worker run" pointer. The dispatcher's rule:

```
if task.session_id is non-null:
    run.session_id := task.session_id
else:
    run.session_id := newSession()
    task.session_id := run.session_id
```

To force a fresh session on retry (e.g. when the conversation has gone
off the rails), clear `task.session_id` before re-dispatch. No mode column
— null means fresh, non-null means reuse.

## Worker contract

A worker, given a `RunnerFunc` and a `TaskControlTool`, must call exactly
one of:

- `tool.Progress(patch)` — shallow-merge JSON patch into `task.context`. Can be
  called multiple times. Does **not** finalize.
- `tool.Submit(output)` — task → `done`, run → `completed`.
- `tool.Block(kind, question, detail)` — task → `blocked`, run → `cancelled`.
- `tool.Fail(reason, retryable)` — run → `failed`; task → `ready` if retry
  budget remains, else `failed`.

If the runner returns without calling a terminal action, the worker
applies a **protocol-error fallback**: the run is marked `failed`, an
event with `event_type='protocol_error'` is written, and the task is
returned to `ready` if the retry budget allows.

Panics in the runner are converted to non-retryable `Fail`.

## Heartbeat + lease

Active runs carry `lease_expires_at` (default 90s) and `heartbeat_at`.
The worker's heartbeat goroutine extends the lease every 20s. The
dispatcher's stale-run sweep finds rows with `status IN ('queued','running')
AND lease_expires_at < now`, marks them `interrupted`, and returns the
task to `ready` if the retry budget allows.

This is how the system recovers from worker crashes or process restarts:
the lease eventually expires, the next tick reclaims the task.

## Invariants (DB-enforced)

These are partial unique indexes or CHECK constraints, not application
discipline:

- `uniq_active_worker_run` — at most one queued/running worker run per task
- `uniq_task_run_attempt` — `(task_id, kind, attempt_no)` unique
- `uniq_open_blocker_per_task` — at most one open blocker per task
- `uniq_active_dispatch_hint_task` — at most one live hint per `(task_id, kind)`
- `agent_task.active_run_id` / `active_blocker_id` use `ON DELETE RESTRICT`
  so the transition service must clear pointers before deleting children

## Boot wiring

`tasks.New(BootConfig{...})` returns a `*tasks.Service` containing the
queries, transition service, facade, and dispatcher. cmd/stella
constructs one at boot and registers `Dispatcher.Tick` as a recurring
in-memory task on the existing `scheduler.Service` (no `sched_job` row).
`Dispatcher.Stop` drains workers before tear-down.

## Status today

Slice 1 (MVP) ships:

- Full schema, transition service, readiness compute, worker, dispatcher
- Service facade (`tasks.ServiceFacade`) for programmatic use
- Boot wiring; dispatcher tick registered on scheduler

**Pending follow-ups (stacked PRs):**

- Real agent.Pool ↔ RunnerFunc adapter for **all four** run kinds
  (worker / reviewer / planner / synthesizer). The dispatcher now
  creates each kind of run; the runners themselves are noop fallbacks
  that immediately emit a `protocol_error` event.

See [Goal system](./goal-system) for the goal-side details.

## HTTP surface

All routes are flat under `/api/tasks/...` and scoped via the
authenticated session.

| Method | Path                                                 | Purpose                                    |
| ------ | ---------------------------------------------------- | ------------------------------------------ |
| POST   | `/api/tasks`                                         | Create a task                              |
| GET    | `/api/tasks`                                         | List tasks (optional `agent_id`, `status`) |
| GET    | `/api/tasks/{id}`                                    | Fetch a task                               |
| POST   | `/api/tasks/{id}/cancel`                             | Cancel a task                              |
| POST   | `/api/tasks/{id}/reopen`                             | Reopen done/failed (`cascade` body)        |
| GET    | `/api/tasks/{id}/readiness`                          | Computed readiness view                    |
| GET    | `/api/tasks/{id}/events`                             | Audit log                                  |
| GET    | `/api/tasks/{id}/runs`                               | Run attempts                               |
| GET    | `/api/tasks/{id}/deps` / POST same                   | Dep edges + add                            |
| POST   | `/api/tasks/{id}/deps/{depTaskID}/waive`             | Waive a hard dep failure                   |
| POST   | `/api/tasks/{id}/blockers/{blockerID}/resolve`       | Resolve a blocker                          |
| GET    | `/api/tasks/{id}/reviews`                            | List reviews                               |
| POST   | `/api/tasks/{id}/reviews/{reviewID}/approve`         | Human-approve a review                     |
| POST   | `/api/tasks/{id}/reviews/{reviewID}/reject`          | Reject                                     |
| POST   | `/api/tasks/{id}/reviews/{reviewID}/request-changes` | Request changes                            |
| POST   | `/api/tasks/{id}/reviews/{reviewID}/escalate`        | Escalate agent review to human             |

Typed error codes:

| Condition                        | HTTP | Code                                            |
| -------------------------------- | ---- | ----------------------------------------------- |
| Unknown task / blocker / review  | 404  | `not_found`                                     |
| Invalid lifecycle transition     | 409  | `invalid_transition`                            |
| Dep edge would close a cycle     | 409  | `dep_cycle`                                     |
| Review already resolved          | 409  | `review_closed`                                 |
| Dep-failure blocker (use waiver) | 409  | `dep_failure_requires_waiver`                   |
| Blocker not open                 | 409  | `blocker_already_closed`                        |
| Reopen would orphan downstream   | 409  | `reopen_conflict` (body lists `downstream_ids`) |
