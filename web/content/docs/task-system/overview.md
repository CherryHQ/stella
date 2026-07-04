---
title: Goals Overview
---

Goals are for outcomes that need tracked, iterative execution instead of a single chat answer.

A **goal** is one outcome an agent drives to acceptance. It carries the intent (what "done" means), an acceptance contract (how the system checks the work), and a record of every attempt. A goal is recursive:

- A **root** goal is your objective.
- **Child** goals are sub-goals it was decomposed into.
- Each goal is either a **leaf** (a worker runs it directly) or **composite** (planned into children first).

## Completion is derived, not declared

The agent never marks a goal "done." It submits **evidence** — the work it produced — and the system checks that evidence against the goal's **acceptance contract**:

- **Checks** run automatically (a command exits cleanly, a file exists, tests pass). The system reads the result.
- **Judgments** ask an agent or a human to decide against a rubric and record a verdict.

If the contract passes, the goal becomes **Accepted** and its output is frozen for anything that depends on it. If it doesn't, the gaps feed into the next attempt. This is why you set a clear definition of done up front — it is what the work is measured against.

## The convergence loop

A leaf goal runs as a bounded rework loop, not a one-shot:

1. Dependencies satisfied → the goal becomes **Active**.
2. The agent works in a one-shot internal execution session and submits evidence.
3. The system evaluates the acceptance contract; deterministic checks run in that same sandbox session, so the command sees the files and environment the agent just used.
4. Pass → **Accepted**. Fail → the gaps become input to the next attempt — that is the rework.
5. Out of attempts → **Blocked** so you can retry, add human guidance on the timeline, or abandon it.

Each attempt is preserved, but the Web UI treats execution sessions as internal plumbing. The goal **Timeline** is the human-readable trail: attempt 1 produced X, acceptance found gaps Y, you added guidance Z, attempt 2 was accepted.

## Lifecycle states

A goal moves through:

- **Draft** — created, not yet activated.
- **Ready** — eligible to run once dependencies and scheduling allow.
- **Active** — the agent is working on it.
- **Blocked** — paused for your verdict, a failed dependency, exhausted budget, unavailable environment, or a contract conflict.
- **Needs review** — evidence submitted, waiting on a judgment verdict.
- **Accepted** — the acceptance contract passed. (Derived, terminal.)
- **Rejected** — closed with no rework path.
- **Abandoned** — you gave up on it for good.
- **Cancelled** — you stopped it.

## Decomposition

A composite goal can't run directly — it must be planned into children first. A decomposition (a plan: the child goals and the dependencies between them) is authored, then — depending on the review policy — auto-accepted or held for your approval. Once accepted, the children are materialized and each runs its own convergence loop.

> **Status.** Plan authoring is now automatic for newly created goals: Stella decomposes a root goal into verifiable children and runs them through the dispatcher. When a goal uses a human review policy, Stella parks it for approval in the Tasks UI before materializing the children.

When all required children are accepted, the parent runs **its own** acceptance evaluation — the same gate every goal has, applied at the root. There is no separate "final synthesis" step; it falls out of the recursion.

## Dependencies

Dependencies make ordering visible and carry **accepted output** downstream — a child only sees an upstream sibling's output once that sibling is accepted.

Use a dependency when:

- One goal needs another's accepted output.
- A downstream goal should stop if an upstream one fails.
- You want the readiness view to explain exactly what is still waiting.

A failed hard dependency blocks the downstream goal until you waive it with a reason. A dependency block is not retryable from the downstream goal because the downstream input has not changed.

## Review and judgment

Some outcomes should stop for a human decision. Route them through a **judgment** item with `human` authority for:

- Policy exceptions.
- Candidate recommendations.
- Release approvals.
- Customer-facing replies.
- Anything that changes money, access, or reputation.

A judgment verdict is still recorded as **evidence** — an approval with its rationale, scope, and authority — not a manual status flip. The system derives acceptance from it.

## The Goals surface

Chat history is a bad project tracker. In the Web UI, open an agent and choose the **Goals** tab to see root goals, scheduled work, and their children in one place. Projects open on their goal list first.

Open a composite goal to inspect its **workflow canvas**. The canvas shows the plan, child goals, dependency edges, and final acceptance as one flow. Green edges are satisfied, animated inbound edges point at running work, and warning rings show the node that needs attention.

Click a node to open its detail dialog:

- **Plan** — root intent, overview fields, and plan revisions.
- **Child goal** — status, intent, dependencies, attempts, readiness, and any required verdict. Composite children link to their own canvas.
- **Acceptance** — contract editor, acceptance ledger, and accepted deliverables after completion.
- **Timeline** — opened from the header button; shows plan events, attempts, acceptance results, lifecycle changes, and your messages.

Dialog state is shareable: `?node=plan`, `?node=accept`, `?node=activity`, or `?node=<child-id>` opens the matching dialog directly. Leaf goals skip the canvas and show their attempts list, with the same acceptance and timeline dialogs available from the header.

The **Needs you** list only shows blocks with a human recovery action. Goals waiting on upstream stay in **Active work** and resume automatically when upstream completes.

Blocked cards show the one-line cause and only useful actions:

- **Environment unavailable** — mark the environment fixed to retry, or report an administrator.
- **Contract conflict** — edit the acceptance contract.
- **Budget exhausted** — retry, or abandon it.
- **Waiting on upstream** — shown in Active work; retry is intentionally hidden.

When a goal is blocked on you, its page shows what it needs — submit your verdict, review a plan, fix a contract, or add guidance on the timeline. The agent picks the work back up with your input when the block is retryable.

The practical rule: use chat to describe outcomes and decisions; use goals to track execution.
