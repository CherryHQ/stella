# Goal / task system

Durable, async, DAG-scheduled work that survives restarts. This is how you do work that outlives a single conversation: long research, multi-step builds, anything that may pause for input or need approval before it counts as done.

## The two roles you play

You touch this system from **two completely different sides**. Know which one you're in.

1. **Manager** — in a normal conversation. You create and steer tasks with the `stella task` CLI (via `bash`). You don't execute the work here; you queue it and check on it.
2. **Worker** — you were _dispatched_ to execute a task. The task's title+description arrives as your prompt in a fresh session, and you get a `task_control` tool. You **must** end by calling `task_control` with `submit`, `block`, or `fail`. Exit without it and the run is a protocol error.

If you see a `task_control` tool in your toolset, you are a worker. Otherwise you are a manager and drive everything through the CLI.

## Pick the right primitive

- `delegate` — synchronous focused subtask, fresh context, returns inline. No persistence, can't pause.
- `task` — async, durable, survives restarts, can block on input, can require review.
- `scheduler` — a _time trigger_, not the work. For long/reviewable scheduled work, schedule a prompt that **creates a task**; don't cram the work into the job.

## Concept model

```
goal ──rolls up from──▶ task ──one attempt──▶ run
                          │
                          ├─ dep edge ──▶ another task   (DAG)
                          ├─ blocker   ──▶ why it paused
                          └─ review    ──▶ approval gate before "done"
```

| Entity   | What it is                                                                                                                   |
| -------- | ---------------------------------------------------------------------------------------------------------------------------- |
| Goal     | High-level objective; container whose status rolls up from child tasks. Not directly dispatched. Managed with `stella task goal`. |
| Task     | Smallest executable unit. Has a strict lifecycle status.                                                                     |
| Run      | One execution attempt. Carries the session, heartbeat, lease.                                                                |
| Dep edge | DAG link. `hard` (default) or `soft`, with an `on_failure` policy.                                                           |
| Blocker  | Records why a task is paused. At most one open per task.                                                                     |
| Review   | Approval gate. Policy decides whether/who reviews before `done`.                                                             |
| Event    | Append-only audit row. Every transition writes one.                                                                          |

Tasks are owned by the resolved org (profile `X-Stella-Org-ID`); they don't nest under an agent path.

## Lifecycle

```
draft ──activate──▶ ready ──claim──▶ running ──submit──▶ (review?) ──▶ done
  │                   │                │
  │                   │                ├─ block ──▶ blocked ──resolve──▶ ready
  │                   │                └─ fail ───▶ ready (retry) or failed
  └─ cancel ──▶ cancelled
```

| Status      | Meaning                                                   |
| ----------- | --------------------------------------------------------- |
| `draft`     | Created, not yet activated. Does nothing until activated. |
| `ready`     | Queued for the dispatcher.                                |
| `running`   | A worker is executing.                                    |
| `blocked`   | Paused on a question or unmet/failed dependency.          |
| `reviewing` | A submit is awaiting review approval.                     |
| `done`      | Completed and approved.                                   |
| `failed`    | Out of retries, or a review was rejected.                 |
| `cancelled` | Stopped by a human.                                       |

**Status is business lifecycle only.** Whether a `ready` task runs _right now_ is a separate computed view — see Readiness.

---

## Manager: the `stella task` CLI

```
stella task list [--agent <id>] [--status <s>]        # list org tasks
stella task get <task-id>                             # details (status, review policy, ids)
stella task create --title <t> [flags]               # create (flags below)
stella task cancel <task-id> [--reason <r>]          # cancel
stella task reopen <task-id> [--cascade]             # reopen done/failed
stella task readiness <task-id>                      # why it is / isn't dispatchable
stella task events <task-id>                         # audit log
stella task deps <task-id>                           # dependency edges
stella task review approve|reject|request-changes <task-id> <review-id> [--summary <s>] [--feedback <f>]
```

### create flags

| Flag            | Purpose                                                          |
| --------------- | ---------------------------------------------------------------- |
| `--title`       | Required.                                                        |
| `--description` | Instructions; becomes the worker's prompt body.                  |
| `--agent`       | Creator agent ID (optional).                                     |
| `--executor`    | Pin a specific executor agent (written as a dispatch hint).      |
| `--priority`    | `routine` (default) or `urgent`.                                 |
| `--dep`         | Dependency edge; repeatable. `<task-id>[:kind[:on_failure]]`.    |
| `--activate`    | Activate now (`draft → ready`). Without it the task stays draft. |

The CLI does **not** set `review_policy`, `not_before`, `deadline`, `max_retries`, or `required` — those come from server defaults / the API / goal context. To build a dependency graph, create the upstreams first, then create downstreams with `--dep`, then activate.

```
# fire-and-forget
stella task create --title "Summarize Q3 competitors" --description "..." --activate
# B waits for A to succeed (hard/block is the default)
stella task create --title "B" --dep <A-id> --activate
```

---

## Worker: the `task_control` tool

When dispatched, the task's title+description is your prompt. Do the work, then call `task_control` exactly once with a terminal action. `progress` is optional and repeatable.

| action     | Effect                                                                                 |
| ---------- | -------------------------------------------------------------------------------------- |
| `progress` | Shallow-merge `patch` JSON into `task.context`. Checkpoint freely; not terminal.       |
| `submit`   | Write `output` JSON to `task.output`. Routes through review policy.                    |
| `block`    | Pause: needs `kind` + `question`. Task → `blocked` until resolved.                     |
| `fail`     | `reason` + `retryable`. Retryable returns to `ready` if budget remains; else `failed`. |

Blocker `kind` values: `user_input`, `external_dependency`, `tool_error`, `policy_hold`. (`dep_failure` is system-generated, never yours.)

Rules:

- **Always end with submit/block/fail.** Returning without one = `protocol_error` → run fails.
- Block when you genuinely need a human/external thing — don't guess and submit garbage.
- Fail `retryable=true` for transient errors (rate limit, flaky tool); `false` for logic errors a retry won't fix.

---

## Dependencies

Edge: `<task-id>[:kind[:on_failure]]`. Defaults `hard` + `block`.

- `kind`: `hard` — upstream must succeed; `soft` — proceed once upstream is terminal in any state (`done`/`failed`/`cancelled`).
- `on_failure` (hard only): `block` (default) → downstream `blocked` on upstream failure; `fail` → downstream `failed`; `ignore` → treat failure as satisfied.

A `hard`/`block` edge whose upstream **failed** parks the downstream in `blocked` with a `dep_failure` reason. This can't be cleared by a normal resolve — it needs an attributable **waiver** (Web UI / API), not a CLI flag. Soft deps never trigger the waiver flow.

## Readiness, not status

`status='ready'` ≠ "will run now." `stella task readiness <id>` prints `dispatchable: true/false` plus reasons. A ready task waits when: `not_before` is future (`deferred`), a hard dep isn't satisfied (`waiting_deps`), org concurrency cap is hit (`throttled`), it's `blocked`, or no executor resolves. Use this to tell "needs input" apart from "waiting on an upstream."

## Reviews

A worker's `submit` routes on the task's `review_policy`:

| Policy  | Behavior                                                        |
| ------- | --------------------------------------------------------------- |
| `none`  | → `done` immediately. No review row.                            |
| `auto`  | System-approved review row for audit, → `done`.                 |
| `agent` | Opens an agent review → `reviewing`; an agent reviewer decides. |
| `human` | Opens a human review → `reviewing`; awaits a human decision.    |

Decide a pending review:

```
stella task review approve <task-id> <review-id> --summary "ok"
stella task review reject <task-id> <review-id> --feedback "wrong dataset"   # task → failed
stella task review request-changes <task-id> <review-id> --feedback "add tests"  # back for rework
```

Get the `review-id` from `stella task get` / the Web UI. Approve resumes; reject fails the task; request-changes sends it back with feedback.

## Goals

A goal groups related tasks and rolls its status up from them (e.g. all required children done → goal done; a child failing a hard gate can fail the goal). Goals live under `stella task goal`:

```
stella task goal list [--limit <n>]                  # list org goals
stella task goal get <goal-id>                       # goal details
stella task goal create --title <t> [flags]          # create (--description, --priority, --review-policy auto|agent|human, --activate)
stella task goal activate <goal-id>                  # draft -> ready
stella task goal cancel <goal-id> [--reason <r>]     # cancel
stella task goal tasks <goal-id>                     # list child tasks
stella task goal review approve|reject|request-changes|escalate <goal-id> <review-id> [--summary] [--feedback] [--reason]
```

A goal is not dispatched directly — it advances as its child tasks complete. Note: planner/synthesizer run kinds are still noop fallbacks, so splitting a goal into child tasks is something you do explicitly (create tasks, then attach them via the goal), not something the system auto-plans yet.

## Recovery

Runs carry a lease (~90s) and heartbeat (extended ~every 20s). If a worker crashes or the process restarts, the lease expires and the next dispatcher tick reclaims the task — back to `ready` if retry budget remains. Nothing is lost; work resumes from the last recorded state. This is why a task can be trusted with hours-long work.
