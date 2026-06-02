---
title: Background Tasks
---

Stella can run tracked work in the background while you continue chatting. Tasks persist across restarts: if Stella reboots, the task state, run history, blockers, and events remain in the database.

## What are tasks?

A task is one executable unit of work outside your current conversation. Use tasks for work that takes time, needs a retryable run history, may pause for input, or should pass through review.

Every task has a lifecycle:

- **Draft** -- created but not yet activated.
- **Ready** -- eligible for dispatch once dependencies and scheduling gates allow it.
- **Running** -- Stella is actively working on it.
- **Blocked** -- Stella paused because it needs input, an external dependency, or a policy hold.
- **Reviewing** -- Stella submitted output and is waiting for the configured review decision.
- **Done** -- completed successfully.
- **Failed** -- the retry budget was exhausted, the worker reported a non-retryable failure, or a review rejected it.
- **Cancelled** -- you stopped the task.

`Ready` does not always mean "running now." A task may still be waiting for a dependency, a future start time, worker capacity, or an executor. The task detail view shows readiness reasons.

## Creating a task

Create a standalone task when the work is independent. Create a goal first when several tasks belong to one larger outcome, then attach child tasks to it.

A typical goal workflow is:

1. Create the goal.
2. Create child tasks with the goal ID.
3. Add dependencies between tasks when order matters.
4. Activate the goal and tasks.
5. Watch task events, blockers, runs, and reviews as work progresses.

Stella does not automatically split a goal into child tasks yet. If you want a multi-step workflow, create the child tasks explicitly from the Web UI, CLI, or an agent command.

## Checking progress

You can check on work at any time:

- **Task list** -- see task status and filters.
- **Task detail** -- inspect readiness, event history, runs, blockers, reviews, and dependencies.
- **Goal detail** -- inspect the child tasks and the goal's rollup status.

Stella can notify you when a task finishes, fails, blocks, or needs review, depending on your channel setup.

## Responding to blockers

When Stella cannot continue safely, the task moves to **blocked**. The blocker explains what is needed.

Common blockers:

- Missing user input.
- External dependency not available yet.
- Tool or service error.
- Policy hold.
- Failed upstream dependency.

Resolve normal blockers by answering the question. A failed-dependency blocker is different: you must waive the dependency with a reason before the downstream task can continue.

## Reviews

Task review policies currently supported by the worker runtime:

- **none** -- output is accepted immediately and the task becomes done.
- **auto** -- Stella records an automatic approval for audit, then marks the task done.
- **human** -- Stella waits for a human approval, rejection, or request for changes.

Agent-performed review is rejected by the API in this release. Use human review when judgment or approval matters.

## Goals

A goal is a container for related tasks. It rolls up from child task states:

- All required children done → goal done.
- A required child failed → goal failed.
- A required child blocked → goal blocked.
- Pending children → goal remains in progress.

Goal final synthesis and goal reviews are rejected in this release. Use goals as containers, and create the child tasks explicitly.

## Worker completion

When Stella dispatches a task worker, the worker must finish with one terminal outcome:

- `submit` when the task is complete.
- `block` when it needs input or an external dependency.
- `fail` when it cannot complete the work.

Progress updates may be recorded while the task runs. If a worker replies with plain text but forgets the terminal action, Stella gives it one correction turn to call `submit`, `block`, or `fail`. If it still exits without a terminal outcome, the run is treated as a protocol failure and may be retried until the retry budget is exhausted.
