# Task system

Stella's task system manages durable background work with a goal/task hierarchy, DAG dependencies, run tracking, and structured review.

## Goals vs tasks

A **goal** is a high-level objective that gets split into child **tasks**. Goals don't run directly — they complete when their required children finish. A **task** is an atomic unit of work assigned to a worker agent.

Create a goal when the work has multiple steps that may depend on each other. Create a standalone task when the work is self-contained.

## Lifecycle

### Task states

```
draft → ready → running → reviewing → done
                  ↓          ↓
               blocked    changes_requested → ready (auto-retry)
                  ↓
               failed / cancelled
```

- **draft**: Created but not yet actionable (e.g., during planning)
- **ready**: All dependencies met, eligible for dispatch
- **blocked**: Worker hit an obstacle needing human input
- **running**: Worker agent is executing
- **reviewing**: Work submitted, awaiting review
- **changes_requested**: Reviewer asked for fixes (auto-retries if retry budget remains)
- **done**: Work completed and approved
- **failed**: Exhausted retries or unrecoverable error
- **cancelled**: Manually cancelled

### Goal states

Goals mirror the task states but transition based on children:

- **running**: Planning or children in progress
- **done**: All required children done
- **failed**: A required child failed with no retry budget

## Dependencies (DAG)

Tasks can declare dependencies on other tasks via `deps`. A task stays in `ready` but won't dispatch until all its deps are `done`. Dependencies are validated for cycles at creation time.

Use `stella task deps add <task-id> <dep-id>` to add a dependency, and `stella task deps rm` / `stella task deps info` to manage them.

## Runs

Each time a task is picked up for execution, a **run** is created. Runs track the agent session, timing, and outcome independently from the task's business status.

Run kinds: `manager_run`, `worker_run`, `reviewer_run`

Run purposes: `planning`, `synthesis`, `replan`, `execution`, `review`, `auto_approval`, `failure_assessment`

Run statuses: `queued`, `running`, `completed`, `failed`, `cancelled`, `interrupted`

A task can have multiple runs across retries. Use `stella task runs <task-id>` to inspect run history.

## Review workflow

### Review policies

Each task has a `review_policy`:

- **auto** (default): Work is auto-approved when the worker submits
- **agent**: A reviewer agent evaluates the work against acceptance criteria
- **human**: A human must approve via the UI or CLI

### Acceptance criteria

Tasks can have acceptance criteria — checklist items that reviewers evaluate. Each criterion is marked required or optional. Create them with `stella task criteria <task-id>` or via the split endpoint.

### Review flow

1. Worker submits work via `submit_for_review`
2. System creates a review record
3. If `auto` policy → auto-approved, task moves to `done`
4. If `agent` or `human` → task moves to `reviewing`, reviewer evaluates criteria
5. Reviewer submits a decision: `approved`, `changes_requested`, or `rejected`
6. On `changes_requested`: task retries if retry budget allows, otherwise fails

## Worker contract

Workers interact with their task through a control tool with these actions:

- **progress**: Report incremental progress (message shown to user)
- **block**: Signal that human input is needed (task → blocked)
- **submit_for_review**: Submit completed work for review
- **failed**: Report unrecoverable failure

Workers cannot mark tasks as `done` directly — that happens through the review pipeline.

## Role boundaries

| Role | Can do | Cannot do |
|------|--------|-----------|
| Manager | Create goals, split into tasks, trigger planning/synthesis runs | Execute task work directly |
| Worker | Execute task work, report progress, submit for review | Mark done, approve own work, create tasks |
| Reviewer | Evaluate criteria, approve/reject work | Modify task content, re-execute work |

## CLI commands

```
stella task list [--status <s>]          # List tasks (shows type, status, priority)
stella task get <id>                     # Get task details
stella task create --title <t>           # Create a standalone task
stella task action <id> --type <action>  # Take action (approve/reject/respond/cancel)
stella task events <id>                  # List task events
stella task runs <id>                    # List runs for a task
stella task reviews <id>                 # List reviews for a task
stella task criteria <id>               # List acceptance criteria
stella task split <goal-id> -f <json>   # Split a goal into child tasks
stella task plan-ready <id>             # Signal that planning is complete
stella task reopen <id>                 # Reopen a done/failed/cancelled task
stella task batch -f <json>             # Create multiple tasks with intra-batch deps
stella task deps add <id> <dep-id>      # Add a dependency
stella task deps rm <id> <dep-id>       # Remove a dependency
stella task deps info <id>              # Show upstream and downstream deps
```

All task commands require `--agent-id <id>` or the `STELLA_AGENT_ID` environment variable.
