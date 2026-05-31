# Goal / task system

Durable, async, DAG-scheduled work that survives restarts. This is how you do work that outlives a single conversation: long research, multi-step builds, anything that may pause for input or need approval before it counts as done.

This file is about **when to reach for which command and how to chain them**. It does not spell out flags — every command is self-documenting, so run `stella task <subcommand> --help` (and `stella task goal <subcommand> --help`) before invoking one.

## The two roles you play

You touch this system from **two completely different sides**. Know which one you're in.

1. **Manager** — in a normal conversation. You create and steer work with the `stella task` CLI (via `bash`). You don't execute the work here; you queue it and check on it.
2. **Worker** — you were _dispatched_ to execute a task. The task's title+description arrives as your prompt in a fresh session, and you get a `task_control` tool instead of the CLI.

If you see a `task_control` tool in your toolset, you are a worker. Otherwise you are a manager.

## Pick the right primitive

Before creating a task at all, check you actually need one:

- `delegate` — synchronous focused subtask, fresh context, returns inline. No persistence, can't pause. Reach for this first for short research/review/drafting.
- `task` — async, durable, survives restarts, can block on input, can require review. Use when work outlives the conversation or needs an approval gate.
- `scheduler` — a _time trigger_, not the work. For long/reviewable scheduled work, schedule a prompt that **creates a task**; don't cram the work into the job.

## Concept model

```
goal ──rolls up from──▶ task ──one attempt──▶ run
                          │
                          ├─ dep edge ──▶ another task   (DAG)
                          ├─ blocker   ──▶ why it paused
                          └─ review    ──▶ approval gate before "done"
```

- **Goal** — container; status rolls up from child tasks. Not dispatched directly. Managed under `stella task goal`.
- **Task** — smallest executable unit, with a strict lifecycle status.
- **Run** — one execution attempt; carries the session, heartbeat, lease.
- **Dep edge** — DAG link, `hard` (default) or `soft`, with an `on_failure` policy.
- **Blocker** — why a task is paused; at most one open per task.
- **Review** — approval gate before `done`; the task's policy decides who reviews.

Tasks are not nested under an agent — they are top-level resources.

## Lifecycle

```
draft ──activate──▶ ready ──claim──▶ running ──submit──▶ (review?) ──▶ done
  │                   │                │
  │                   │                ├─ block ──▶ blocked ──resolve──▶ ready
  │                   │                └─ fail ───▶ ready (retry) or failed
  └─ cancel ──▶ cancelled
```

Key point: a task does nothing until **activated** (`draft → ready`). And `ready` ≠ "running now" — see Readiness.

---

## Manager playbooks

All of these are `stella task ...` (or `stella task goal ...`) commands; run `--help` for the exact flags.

**Fire-and-forget a single task.** `create` with `--activate`. Without `--activate` it stays `draft` and never runs — use draft only when you're building a graph and want to wire deps first.

**Build a dependency graph (DAG).** Create the upstream tasks first (note their IDs), then `create` the downstream with one or more `--dep <upstream-id>` edges, then `activate` everything. To add an edge after the fact, use `dep add`. Default edge is `hard` + `block`: the downstream waits for the upstream to _succeed_.

**Check on work.** `list` to scan tasks (filter by status/agent); `get <id>` for full detail including status, review policy, and the `active_blocker` / `active_review` / `active_run` IDs you'll need for follow-up commands. `events` is the audit trail; `runs` is the per-attempt history.

**"Why isn't this running?"** Don't trust status alone — run `readiness <id>`. It prints `dispatchable: true/false` and the reason, so you can tell "waiting on an upstream" (`waiting_deps`) apart from "needs input" (`blocked`), "scheduled for later" (`deferred`), "cap hit" (`throttled`), or "no executor".

**Answer a worker that blocked.** A worker pauses by raising a blocker (a question or an external dependency). To resume it: `get <id>` to read the question and grab the `active_blocker` ID, then `blocker resolve <id> <blocker-id> --resolution "..."`. Your resolution text is the answer the worker sees on resume.

**Clear a failed dependency.** If an upstream _fails_, a `hard`/`block` downstream parks in `blocked` with a `dep_failure` reason. This one is **not** clearable by `blocker resolve` — it needs an attributable `dep waive <id> <dep-task-id> --reason "..."`. (Soft deps never get here.)

**Run a review decision.** When a task's policy opens a review, the task sits in `reviewing`. `reviews <id>` lists them (source of the review-id; or use `active_review` from `get`), then `review approve|reject|request-changes|escalate`. See Reviews below for what each does.

**Retry / undo.** `cancel` stops a task (human action). `reopen` brings a `done`/`failed` task back; `--cascade` to pull dependents with it.

## Reviews

A worker's `submit` routes on the task's `review_policy`:

- `none` → `done` immediately, no review row.
- `auto` → system-approved review row for audit, → `done`.
- `agent` → opens an agent review (`reviewing`); an agent reviewer decides.
- `human` → opens a human review (`reviewing`); awaits a human.

Decisions and their effect:

- `approve` — resumes toward `done`.
- `reject` — task → `failed`.
- `request-changes` — sends it back for rework with your feedback.
- `escalate` — hands an agent review up to a human.

Goal reviews mirror this under `stella task goal review ...` (`stella task goal reviews <goal-id>` lists them).

## Goals

A goal groups related tasks and rolls its status up from them (all required children done → goal done; a child failing a hard gate can fail the goal). Use a goal when several tasks serve one objective and you want a single rollup + review gate.

Workflow: `goal create` the container, `goal create`/`task create` the child tasks, then activate. Inspect with `goal get` and `goal tasks`; review with `goal reviews` + `goal review`.

Caveat: the system does **not** auto-split a goal into child tasks yet (planner/synthesizer are noop fallbacks). You create the tasks and attach them explicitly.

---

## Worker: the `task_control` contract

When dispatched, the task's title+description is your prompt. Do the work, then call `task_control` exactly once with a terminal action — `submit`, `block`, or `fail`. `progress` is optional and repeatable for checkpointing.

- `progress` — shallow-merge a `patch` into `task.context`. Checkpoint freely; not terminal.
- `submit` — write `output` JSON; routes through the review policy.
- `block` — pause for something you genuinely can't do yourself; needs `kind` + `question`. Kinds: `user_input`, `external_dependency`, `tool_error`, `policy_hold`. (`dep_failure` is system-only.)
- `fail` — `reason` + `retryable`. `true` for transient errors (rate limit, flaky tool); `false` for logic errors a retry won't fix.

Rules:

- **Always end with submit/block/fail.** Returning without one = `protocol_error` → the run fails.
- Block when you truly need a human/external thing — don't guess and submit garbage.

## Recovery

Runs carry a lease (~90s) and heartbeat. If a worker crashes or the process restarts, the lease expires and the next dispatcher tick reclaims the task — back to `ready` if retry budget remains. Work resumes from the last recorded state, which is why a task can be trusted with hours-long work.
