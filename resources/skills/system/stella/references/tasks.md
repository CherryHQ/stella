# Goal / task system

Durable, async work that survives restarts. Use this system for work that outlives a single conversation: long research, multi-step builds, work that may pause for input, and work that needs human approval before it counts as done.

This file is about **when to reach for which tool and how to chain them**. The `task_*` and `task_goal_*` tools carry their own parameter schemas — read each tool's description before calling it.

## Current supported shape

Supported now:

- Standalone tasks.
- Goals as containers for explicitly created child tasks.
- Task dependencies.
- Worker execution with `task_control`.
- Task review policies: `none`, `auto`, `human`.
- Goal rollup when the goal uses `review_policy=none`.

Not supported now:

- Automatic goal planning / auto-splitting into child tasks.
- Goal final synthesis.
- Goal-level review runtime.
- Agent-performed review (`review_policy=agent`).

Do not promise automatic planning. If a user wants a multi-step goal, create the child tasks explicitly and attach them with each task's `goal_id`.

## The two roles you play

You touch this system from two different sides. Know which one you're in.

1. **Manager** — normal conversation. You create and steer work with the `task_*` and `task_goal_*` tools. You queue work, wire dependencies, and check status.
2. **Worker** — you were dispatched to execute one task. The task title and description arrive as your prompt, and you get a `task_control` tool.

If you see a `task_control` tool in your toolset, you are a worker. Otherwise you are a manager.

## Pick the right primitive

Before creating a task, check you actually need one:

- `delegate` — synchronous focused subtask in a persistent child session, returns inline. Use this first for short research, review, or drafting.
- task (`task_create`) — async, durable, survives restarts, can block on input, can require review. Use this when work outlives the current conversation or needs an approval gate.
- goal (`task_goal_create`) — container for related tasks. Use it when several tasks serve one objective and you need rollup status.
- scheduler (`scheduler_add`) — time trigger, not the work itself. For long or reviewable scheduled work, schedule a prompt that creates a task.

## Concept model

```text
goal ──rolls up from──▶ task ──one attempt──▶ run
                          │
                          ├─ dep edge ──▶ another task   (DAG)
                          ├─ blocker   ──▶ why it paused
                          └─ review    ──▶ approval gate before done
```

- **Goal** — container; status rolls up from child tasks. Managed with `task_goal_*` tools.
- **Task** — smallest executable unit, with a strict lifecycle status.
- **Run** — one execution attempt; records the task's worker session, heartbeat, and lease.
- **Dep edge** — DAG link, `hard` or `soft`, with an `on_failure` policy.
- **Blocker** — why a task is paused; at most one open blocker per task.
- **Review** — approval gate before `done`; task policy decides who reviews.

Tasks and goals run in the current agent's context: the tools read your agent and user identity automatically, so you never pass identity arguments. A task always has a durable worker session minted at creation time. Pass `project_id` when the work should run in a different project than the current session's.

## Lifecycle

```text
draft ──activate──▶ ready ──claim──▶ running ──submit──▶ (review?) ──▶ done
  │                   │                │
  │                   │                ├─ block ──▶ blocked ──resolve/waive──▶ ready
  │                   │                └─ fail ───▶ ready (retry) or failed
  └─ cancel ──▶ cancelled
```

A task does nothing until activated. `ready` means eligible for readiness checks, not necessarily running now.

## Manager playbooks

You drive this with the `task_*` and `task_goal_*` tools.

**Create one background task.** Call `task_create` with `activate:true`. Add `project_id` for project-scoped work. Without `activate:true`, the task stays `draft` and never runs.

**Build a goal.** Call `task_goal_create` first, then create child tasks with `task_create` passing `goal_id`. A task created without `goal_id` is standalone and will not appear under the goal or the Automations goal detail page.

**Build a dependency graph.** Create upstream tasks first, note their IDs, then create downstream tasks with `deps:[<upstream-id>]`. Default dependency behavior is `hard` + `block`: downstream waits for upstream success.

**Activate after wiring.** For multi-task work, create the goal and child tasks, wire `deps`, then activate the tasks. Draft child tasks under an activated goal are promoted to ready.

**Check status.** Use `task_list` to scan, `task_get` for detail, `task_events` for audit history, `task_deps` for dependency edges with upstream status, and `task_goal_get` / `task_goal_list` for goals.

## What the user handles in the Web UI

Some lifecycle operations are not yet exposed as agent tools. Direct the user to the Automations area of the Web UI for:

- **Readiness diagnosis** — why a task is not running (waiting deps, blockers, future `not_before`, throttling).
- **Blocker resolution** — answering or waiving a blocker a worker raised.
- **Reviews** — approving, rejecting, or requesting changes on `human`-policy task output.
- **Reopen / retry** — bringing a `done` or `failed` task back, optionally cascading downstream.
- **Goal activation and inspection** beyond `task_goal_get`.

(These long-tail operations are planned to arrive as agent tools in a later release.)

## Reviews

A worker's `submit` routes on the task's `review_policy`:

- `none` → task becomes `done`, no review row.
- `auto` → system-approved review row for audit, then `done`.
- `human` → opens human review; task stays `reviewing` until a human decides via the Web UI.

Unsupported:

- `agent` → do not use. Agent reviewer runtime is not available.
- Goal-level review → do not use yet. Keep goal `review_policy=none`.

Decision effects (decided by a human in the Web UI):

- `approve` — task moves toward `done`.
- `reject` — task becomes `failed`.
- `request-changes` — task returns to `ready` for rework if retry budget allows; otherwise `failed`.

## Goals

Use a goal when multiple tasks serve one objective and you want a single rollup.

Supported goal workflow:

1. `task_goal_create` with a title (and `description` / `project_id` as needed).
2. `task_create` with `goal_id` set to the new goal's ID.
3. Add dependencies between child tasks via `deps` when needed.
4. Activate the child tasks.
5. Inspect with `task_goal_get`.

Goal rollup:

- all required children done → goal done
- required child failed → goal failed
- required child blocked → goal blocked
- pending child work → goal remains running
- blocked goal recovers → when the blocking child's blocker is resolved or its failed dependency is waived, the goal returns to running on the next rollup; no separate goal-unblock action is needed

Caveat: Stella does **not** auto-split a goal into child tasks yet. Planner and synthesizer runtimes are not supported. You create and attach the child tasks explicitly.

## Worker: the `task_control` contract

When dispatched, do the work and call `task_control` exactly once with a terminal action:

- `progress` — shallow-merge a `patch` into `task.context`. Optional, repeatable, not terminal.
- `submit` — provide `output` JSON when the task is complete.
- `block` — pause because you need input or an external dependency. Include `kind` and `question`.
- `fail` — report `reason` and `retryable`.

Rules:

- Always end with `submit`, `block`, or `fail`.
- Returning text without a terminal action is a protocol error and the run may be retried.
- Block only when you truly need a human or external dependency.
- Do not fake completion just to avoid blocking.

## Recovery

Runs carry a lease and heartbeat. If a worker crashes or Stella restarts, the lease expires and the dispatcher can reclaim the task if retry budget remains. Progress and terminal state are durable because they are written to the task database.
