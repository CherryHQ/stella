---
title: Background Tasks
---

Stella can work on long-running jobs in the background while you continue chatting. Tasks persist across restarts -- even if Stella reboots, your work picks up where it left off.

## What are tasks?

A task is a piece of work that Stella handles independently, outside your current conversation. Tasks are useful when work takes minutes or hours -- research projects, code refactoring, multi-step analysis -- and you do not want to wait around for the result.

Every task has a lifecycle:

- **Draft** -- created but not yet activated
- **Ready** -- queued and waiting to start
- **Running** -- Stella is actively working on it
- **Blocked** -- Stella paused to ask you a question
- **Reviewing** -- Stella finished a phase and wants your approval before continuing
- **Done** -- completed successfully
- **Failed** -- something went wrong, or you rejected a review
- **Cancelled** -- you stopped the task

When a task is waiting on another task to finish (a dependency), the Tasks panel surfaces a **Readiness** view that explains _why_ it isn't running yet — useful to tell "blocked because Stella needs your input" apart from "waiting for an upstream task to finish."

## Creating a task

Ask Stella in any conversation to work on something in the background. For example:

> "Research the top five competitors in the SaaS billing space and summarize their pricing models. Do this as a background task."

Stella creates the task, gives you an ID, and starts working on it. You can keep chatting about other things.

You can also create tasks from the Web UI under the **Tasks** page.

## Checking progress

You can check on a task at any time:

- **In conversation** -- ask Stella something like "What's the status of my research task?" or "Show me my running tasks."
- **In the Web UI** -- open the **Tasks** page to see all your tasks, their current status, and a timeline of events.

Stella also sends you a notification when a task finishes, fails, or needs your attention.

## Responding to questions

Sometimes Stella needs more information before it can continue. When this happens, the task moves to **blocked** and you receive a notification with the question.

You can respond in two ways:

1. **Web UI** -- open the task detail page and type your response in the reply box.
2. **In conversation** -- ask Stella to respond to the blocked task with your answer.

Once you respond, the task resumes automatically.

## Approving work

For important or risky steps, Stella may pause and request your review before proceeding. The task moves to **review requested** and you receive a notification with:

- A description of what was done
- A recommendation
- Options to **approve** or **reject**

If you approve, Stella continues. If you reject (optionally with a reason), the task stops.

You can approve or reject from the Web UI task detail page or by telling Stella in conversation.

## Managing tasks

From the Web UI **Tasks** page, you can:

- View all your tasks and filter by status
- Open a task to see its full event timeline
- Respond to blocked tasks
- Approve or reject reviews
- Cancel a running or pending task

You can also manage tasks through conversation -- ask Stella to list, cancel, or check on your tasks at any time.
