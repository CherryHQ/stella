---
title: Goal system
description: Multi-step plans backed by tasks, with rollup, planner / synthesizer runs, and the review pipeline.
---

The goal system layers a planning + synthesis pass on top of the task system.
A **goal** is a row in `agent_goal` that owns a set of child tasks. The
dispatcher drives goals through draft → planning → running → (reviewing) →
done via four kinds of runs and a pure rollup function.

> Builds on the [Task system](./task-system).

## Mental model

| Concept              | Purpose                                                                                 |
| -------------------- | --------------------------------------------------------------------------------------- |
| `agent_goal`         | Container for a related set of tasks. Has a status + review policy + active review.     |
| `agent_task.goal_id` | A task can belong to one goal (or be standalone).                                       |
| `planner` run        | Generates the goal's draft child tasks. Emitted while the goal is in `draft`.           |
| `synthesizer` run    | Aggregates child outputs into `agent_goal.output`. Emitted after required deps satisfy. |
| `reviewer` run       | Same as task-side; can review goal-parented `agent_review` rows.                        |
| `agent_review`       | One review row per parent (task or goal), XOR.                                          |

## Goal lifecycle

```
draft ── planner ──▶ draft (children created)
  │
  └─ ActivateGoal ──▶ running ── rollup says all required done ─▶ done (policy=none)
                                                              ├▶ reviewing (synthesizer + agent/human review)
                                                              └▶ failed (required child failed)
running ── required child blocked ──▶ blocked
non-terminal ── CancelGoal ──▶ cancelled (cascade-cancels non-terminal children)
```

## Rollup

`internal/tasks/goal_rollup.go` exposes a pure function:

```go
RollupGoal(goal, childCounts, hasOpenSynth) GoalNextState
```

The decision table for a `running` goal:

| Required children | Review policy      | Verdict                                         |
| ----------------- | ------------------ | ----------------------------------------------- |
| any failed        | —                  | NextStatus=failed                               |
| any blocked       | —                  | NextStatus=blocked                              |
| any pending       | —                  | no-op                                           |
| all done          | `none`             | NextStatus=done                                 |
| all done          | `auto/agent/human` | SpawnSynthesizer=true (unless one is in flight) |

Other goal statuses are no-ops at the rollup level — their transitions live
in `ActivateGoal`, review decisions, etc.

## Dispatcher tick

Each tick now runs (after the task-side steps):

1. `rollupGoals` — `RollupGoal` per non-terminal goal; applies the verdict.
2. `scanAndDispatchReviewers` — for every open agent review (task- or
   goal-parented) with no reviewer_run_id yet, create a `reviewer` run and
   attach it via `SetAgentReviewReviewerRun`.
3. `scanAndDispatchPlanners` — for every draft goal with no in-flight
   planner, create one.
4. `scanAndDispatchSynthesizers` — for every running goal whose rollup says
   "spawn synthesizer," create one.

### Noop runner state (current PR)

The reviewer / planner / synthesizer runs are created in the database and
**immediately failed** by `failGoalRunAsNoop` / `failReviewerRunAsNoop` with
a `protocol_error` event. This keeps the dispatch path observable in events
and runs without an `agent.Pool` adapter wired yet. A follow-up PR will
replace the immediate-fail with real runner execution.

What this means in practice:

- `review_policy='agent'` reviews get a reviewer run + go in_progress, then
  the run fails (visible in events as `dispatch_reviewer` + `protocol_error`).
- Draft goals spawn one planner run per tick (each fails). The
  unique-active partial indexes prevent overlap on a single tick.
- Goals stuck waiting for the real runner are easy to spot: filter events
  by `event_type='protocol_error' AND detail->>'reason' LIKE 'noop_%'`.

## Goal review decisions

`ApproveReview` / `RejectReview` / `RequestChanges` dispatch on parent type
(`internal/tasks/review.go:decideAnyReview`):

- Task parent: existing behavior (approve→done, reject→failed,
  request_changes→ready or failed by retry budget).
- Goal parent: approve→`goal.done`, reject→`goal.failed`,
  request_changes→`goal.failed` (documented gap — no goal-level retry
  budget; flagged for follow-up).

`EscalateReview` repoints the matching `active_review_id` (task or goal) at
the new human review row.

## HTTP surface

| Method | Path                                                                              | Purpose                                            |
| ------ | --------------------------------------------------------------------------------- | -------------------------------------------------- |
| POST   | `/api/goals`                                                                      | Create a goal (draft).                             |
| GET    | `/api/goals`                                                                      | List goals.                                        |
| GET    | `/api/goals/{id}`                                                                 | Fetch one goal.                                    |
| POST   | `/api/goals/{id}/activate`                                                        | Draft → running; promotes draft children to ready. |
| POST   | `/api/goals/{id}/cancel`                                                          | Cascade-cancel non-terminal children.              |
| GET    | `/api/goals/{id}/tasks`                                                           | List child tasks.                                  |
| GET    | `/api/goals/{id}/reviews`                                                         | List goal reviews.                                 |
| POST   | `/api/goals/{id}/reviews/{reviewID}/approve` (+reject, request-changes, escalate) | Decide on a goal review.                           |

Access is scoped via the authenticated session.

## Pending follow-ups

- Real `agent.Pool` ↔ runner adapter for reviewer / planner / synthesizer
  runs. Until then, all three dispatch paths emit `protocol_error`.
- Goal-side `request_changes` → synthesizer retry budget (currently
  collapses to `failed`).
- Web UI for goals.
