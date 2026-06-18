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

- Automatic LLM goal planning / auto-splitting into child tasks.
- Goal final synthesis.
- Goal-level review runtime.
- Agent-performed review (`review_policy=agent`).

A goal's child tasks come **only** from a materialized plan — you cannot hand-attach
a task to a goal (`stella task create --goal-id ...` is rejected). For a multi-step
goal, write a structured plan and materialize it (see Goals below).

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

Tasks and goals require the scoped sandbox `STELLA_TOKEN` that Stella injects inside agent sessions. Do not pass or invent an agent ID; the CLI reads the token claims and the server verifies them. A task always has a durable worker session minted at creation time. Use `--project-id` only when the command exposes it as a work-context field.

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

**Let the chat render created entities.** When you create a task or goal, the CLI prints a sideband marker on stderr that the chat turns into a rich, clickable card. Run `stella task create` / `stella task goal create` plainly — do not redirect or discard stderr (no `2>/dev/null`), or the user sees a bare ID instead of a card. The card already shows the title, status, and a link, so do **not** echo the raw ID/title back into your reply text — just say what you did and let the card speak.

**Build a goal.** Create the goal first, optionally with `--project-id`, then create child tasks with `stella task create --goal-id <goal-id> ...`. A task created without `--goal-id` is standalone and will not appear under `stella task goal tasks <goal-id>` or the Web UI goal detail page.

**Build a dependency graph.** Create upstream tasks first, note their IDs, then create downstream tasks with `--dep <upstream-id>` or add edges later with `stella task dep add`. Default dependency behavior is `hard` + `block`: downstream waits for upstream success.

**Activate after wiring.** For multi-task work, wire the goal/tasks/deps first, then activate. Draft child tasks under an activated goal are promoted to ready.

**Check status.** Use `list` to scan, `get <id>` for detail, `events <id>` for audit history, and `runs <id>` for attempts.

**Use the Web UI when the user asks to inspect work visually.** The agent **Tasks** tab shows one-time tasks, scheduled work, and goals together. Project pages open task-first and keep project task rows, the project main conversation, task sessions, and workspace files adjacent.

**Explain why a task is not running.** Use `readiness <id>`. It distinguishes waiting dependencies, blockers, future `not_before`, throttling, terminal state, and missing executor context.

**Answer a blocker.** Use `get <id>` to read the blocker and find `active_blocker`, then resolve it with `blocker resolve --resolution "..."`. Always pass `--resolution`: the answer is delivered to the worker when the task resumes, so an empty resolution makes the worker re-ask the same question. If the blocker is `dep_failure`, do not use generic resolve; waive the dependency with `dep waive <id> <dep-task-id> --reason "..."`.

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

- **Single-step goal (default).** `stella task goal create --title "..."` still
  creates a real plan — the system authors a one-item `direct` plan (titled after
  the goal), accepts it without review, and materializes it automatically, leaving
  the goal `planned`. The single child task named after the goal **is** that plan's
  materialization, not a hand-attached task. Add `--activate` to start it immediately.
  (The plan is readable via `goal plan get` and shown as a Plan section on the Web UI
  goal detail page.)
- **Multi-step goal.** `stella task goal create --title "..." --plan-mode deferred`
  leaves the goal at `draft` with no plan. Then:
  1. `stella task goal plan set <goal-id> --file plan.json` — stage a structured
     plan (`{"items":[{"id","title","role","deps","criteria"}]}`; roles
     `design|impl|verify`). Add `--review-policy human` to require human approval.
  2. Accept it: `stella task goal plan accept <goal-id>` (review_policy none), or
     `plan submit-review` then `plan review approve <goal-id> <review-id>` (human).
  3. `stella task goal plan materialize <goal-id>` — builds the task graph; goal → `planned`.
  4. `stella task goal activate <goal-id>`.
- Inspect with `goal get`, `goal tasks`, and `goal plan get`.

Run `stella task goal plan --help` for the full command set.

Goal rollup:

- all required children done → goal done
- required child failed → goal failed
- required child blocked → goal blocked
- pending child work → goal remains running
- blocked goal recovers → when the blocking child's blocker is resolved or its failed dependency is waived, the goal returns to running on the next rollup; no separate goal-unblock command is needed

Caveat: Stella does **not** auto-split a goal with an LLM yet. Planner and
synthesizer runtimes are not supported — you author the plan content. The plan
gate is enforced, though: child tasks exist only after `plan materialize`, never by
hand-attaching to a goal.

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
