---
title: Task System Overview
---

Stella's task system is for goals that need tracked execution instead of a single chat answer.

A **task** is one executable unit of work. A **goal** is a container that groups related tasks and shows the overall outcome. You create the task graph explicitly: make the goal, create child tasks with `--goal-id`, add dependencies, then activate the work.

## What Stella can do today

Stella can:

- Run activated tasks in the background.
- Persist task, run, event, blocker, review, and goal state across restarts.
- Reuse the task session when a task retries.
- Explain why a task is not running yet with readiness information.
- Block a task when it needs user input or an external dependency.
- Retry transient failures until the retry budget is exhausted.
- Route task output through `none`, `auto`, or `human` review policies.
- Roll a goal up from its child tasks when `review_policy` is `none`.

Stella does **not** automatically plan a goal into tasks yet. It also does not support agent-performed reviews or final goal synthesis yet. Use human review for work that needs judgment.

## Goal to tasks

Start with an outcome:

> Prepare the Q2 reimbursement audit packet and flag anything that needs finance review.

Then create explicit tasks:

- Collect reimbursement records.
- Extract receipt metadata.
- Compare each request against policy.
- Flag exceptions.
- Prepare the review packet.
- Ask finance to review exceptions.

The goal gives you one place to see whether the whole objective is done, blocked, failed, or still in progress.

## Dependencies

Dependencies make ordering visible. The review packet should not run before the policy checks finish.

Use dependencies when:

- One task needs another task's output.
- A downstream task should stop if an upstream task fails.
- You want the readiness view to explain exactly what is still waiting.

## Review and approval

Some work should stop for a human decision. Use human review for:

- Policy exceptions.
- Candidate recommendations.
- Release approvals.
- Customer-facing replies.
- Anything that changes money, access, or reputation.

`auto` review records an automatic approval for audit. `none` completes immediately when the worker submits output. Agent review is not available in this release.

## Task UI

The task UI exists because chat history is a bad project tracker. In the Web UI, open an agent and choose the **Tasks** tab to see one-time tasks, scheduled work, and goals in one place. Project pages also open on their project task list first.

Use the task UI to inspect:

- Current task status.
- Dependencies and readiness.
- Blockers.
- Events and runs.
- Review state.
- Goal child tasks and rollup.
- The conversation session attached to a task or run.

The practical rule: use chat to describe goals and decisions; use tasks to track execution.
