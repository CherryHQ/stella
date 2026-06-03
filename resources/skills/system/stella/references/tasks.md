# Goal / task system

Durable, async work that survives restarts. Use this system for work that outlives a single conversation: long research, multi-step builds, work that may pause for input, and work that needs human approval before it counts as done.

This file is about **when to reach for which command and how to chain them**. It does not spell out every flag — run `stella task <subcommand> --help` and `stella task goal <subcommand> --help` before invoking commands.

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

Do not promise automatic planning. If a user wants a multi-step goal, create the child tasks explicitly and attach them with `--goal-id`.

## The two roles you play

You touch this system from two different sides. Know which one you're in.

1. **Manager** — normal conversation. You create and steer work with the `stella task` CLI via `bash`. You queue work, wire dependencies, check status, resolve blockers, and decide reviews.
2. **Worker** — you were dispatched to execute one task. The task title and description arrive as your prompt, and you get a `task_control` tool.

If you see a `task_control` tool in your toolset, you are a worker. Otherwise you are a manager.

## Pick the right primitive

Before creating a task, check you actually need one:

- `delegate` — synchronous focused subtask in a persistent child session, returns inline. Use this first for short research, review, or drafting.
- `task` — async, durable, survives restarts, can block on input, can require review. Use this when work outlives the current conversation or needs an approval gate.
- `goal` — container for related tasks. Use it when several tasks serve one objective and you need rollup status.
- `scheduler` — time trigger, not the work itself. For long or reviewable scheduled work, schedule a prompt that creates a task.

## Concept model

```text
goal ──rolls up from──▶ task ──one attempt──▶ run
                          │
                          ├─ dep edge ──▶ another task   (DAG)
                          ├─ blocker   ──▶ why it paused
                          └─ review    ──▶ approval gate before done
```

- **Goal** — container; status rolls up from child tasks. Managed under `stella task goal`.
- **Task** — smallest executable unit, with a strict lifecycle status.
- **Run** — one execution attempt; records the task's worker session, heartbeat, and lease.
- **Dep edge** — DAG link, `hard` or `soft`, with an `on_failure` policy.
- **Blocker** — why a task is paused; at most one open blocker per task.
- **Review** — approval gate before `done`; task policy decides who reviews.

Tasks and goals require agent context. Inside Stella sessions, `stella task create` and `stella task goal create` default to `STELLA_AGENT_ID`. Outside Stella, pass `--agent-id` explicitly. A task always has a durable worker session minted at creation time. Use `--project-id` when the work should run in a project/workspace context. If `--goal-id` is set and `--agent-id` is omitted, the task inherits the goal's agent.

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

All commands are `stella task ...` or `stella task goal ...`.

**Create one background task.** Use `stella task create ... --activate`. Add `--project-id <project-id>` for project-scoped work. Without `--activate`, the task stays `draft` and never runs.

**Build a goal.** Create the goal first, optionally with `--project-id`, then create child tasks with `stella task create --goal-id <goal-id> ...`. A task created without `--goal-id` is standalone and will not appear under `stella task goal tasks <goal-id>` or the Automations goal detail page.

**Build a dependency graph.** Create upstream tasks first, note their IDs, then create downstream tasks with `--dep <upstream-id>` or add edges later with `stella task dep add`. Default dependency behavior is `hard` + `block`: downstream waits for upstream success.

**Activate after wiring.** For multi-task work, wire the goal/tasks/deps first, then activate. Draft child tasks under an activated goal are promoted to ready.

**Check status.** Use `list` to scan, `get <id>` for detail, `events <id>` for audit history, and `runs <id>` for attempts.

**Explain why a task is not running.** Use `readiness <id>`. It distinguishes waiting dependencies, blockers, future `not_before`, throttling, terminal state, and missing executor context.

**Answer a blocker.** Use `get <id>` to read the blocker and find `active_blocker`, then resolve it with `blocker resolve`. If the blocker is `dep_failure`, do not use generic resolve; waive the dependency with `dep waive <id> <dep-task-id> --reason "..."`.

**Review task output.** Supported task review policies are `none`, `auto`, and `human`. Use `reviews <id>` to list review rows and `review approve|reject|request-changes` to decide. Do not use `review_policy=agent`; agent reviewer runtime is not supported.

**Retry or undo.** `cancel` stops a task. `reopen` brings a `done` or `failed` task back; use cascade only when you intentionally want to reset downstream work.

## Reviews

A worker's `submit` routes on the task's `review_policy`:

- `none` → task becomes `done`, no review row.
- `auto` → system-approved review row for audit, then `done`.
- `human` → opens human review; task stays `reviewing` until a human decides.

Unsupported:

- `agent` → do not use. Agent reviewer runtime is not available.
- Goal-level review → do not use yet. Keep goal `review_policy=none`.

Decision effects:

- `approve` — task moves toward `done`.
- `reject` — task becomes `failed`.
- `request-changes` — task returns to `ready` for rework if retry budget allows; otherwise `failed`.

## Goals

Use a goal when multiple tasks serve one objective and you want a single rollup.

Supported goal workflow:

1. `stella task goal create --title "..." --review-policy none`
2. `stella task create --goal-id <goal-id> --title "..." ...`
3. Add dependencies between child tasks when needed.
4. Activate the goal/tasks.
5. Inspect with `goal get` and `goal tasks`.

Goal rollup:

- all required children done → goal done
- required child failed → goal failed
- required child blocked → goal blocked
- pending child work → goal remains running
- blocked goal recovers → when the blocking child's blocker is resolved or its failed dependency is waived, the goal returns to running on the next rollup; no separate goal-unblock command is needed

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
