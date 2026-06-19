---
title: Deliverables Overview
---

Deliverables are for outcomes that need tracked, iterative execution instead of a single chat answer.

A **deliverable** is one outcome an agent drives to acceptance. It carries the intent (what "done" means), an acceptance contract (how the system checks the work), and a record of every attempt. A deliverable is recursive:

- A **root** deliverable is your objective.
- **Child** deliverables are sub-deliverables it was decomposed into.
- Each deliverable is either a **leaf** (a worker runs it directly) or **composite** (planned into children first).

## Completion is derived, not declared

The agent never marks a deliverable "done." It submits **evidence** — the work it produced — and the system checks that evidence against the deliverable's **acceptance contract**:

- **Checks** run automatically (a command exits cleanly, a file exists, tests pass). The system reads the result.
- **Judgments** ask an agent or a human to decide against a rubric and record a verdict.

If the contract passes, the deliverable becomes **Accepted** and its output is frozen for anything that depends on it. If it doesn't, the gaps feed into the next attempt. This is why you set a clear definition of done up front — it is what the work is measured against.

## The convergence loop

A leaf deliverable runs as a bounded rework loop, not a one-shot:

1. Dependencies satisfied → the deliverable becomes **Active**.
2. The agent works in its session and submits evidence.
3. The system evaluates the acceptance contract.
4. Pass → **Accepted**. Fail → the gaps become input to the next attempt — that is the rework.
5. Out of attempts → **Blocked** so you can raise the budget or abandon it.

Each attempt is preserved, so the trail reads cleanly: attempt 1 produced X, acceptance found gaps Y, attempt 2 produced Z, accepted.

## Lifecycle states

A deliverable moves through:

- **Draft** — created, not yet activated.
- **Ready** — eligible to run once dependencies and scheduling allow.
- **Active** — the agent is working on it.
- **Blocked** — paused for your verdict, a failed dependency, or an exhausted attempt budget.
- **Needs review** — evidence submitted, waiting on a judgment verdict.
- **Accepted** — the acceptance contract passed. (Derived, terminal.)
- **Rejected** — closed with no rework path.
- **Abandoned** — you gave up on it for good.
- **Cancelled** — you stopped it.

## Decomposition

A composite deliverable can't run directly — it must be planned into children first. The agent authors a decomposition (a plan: the child deliverables and the dependencies between them); depending on the review policy it is auto-accepted or waits for your approval. Once accepted, the children are materialized and each runs its own convergence loop.

When all required children are accepted, the parent runs **its own** acceptance evaluation — the same gate every deliverable has, applied at the root. There is no separate "final synthesis" step; it falls out of the recursion.

## Dependencies

Dependencies make ordering visible and carry **accepted output** downstream — a child only sees an upstream sibling's output once that sibling is accepted.

Use a dependency when:

- One deliverable needs another's accepted output.
- A downstream deliverable should stop if an upstream one fails.
- You want the readiness view to explain exactly what is still waiting.

A failed hard dependency blocks the downstream deliverable until you waive it with a reason.

## Review and judgment

Some outcomes should stop for a human decision. Route them through a **judgment** item with `human` authority for:

- Policy exceptions.
- Candidate recommendations.
- Release approvals.
- Customer-facing replies.
- Anything that changes money, access, or reputation.

A judgment verdict is still recorded as **evidence** — an approval with its rationale, scope, and authority — not a manual status flip. The system derives acceptance from it.

## The Deliverables surface

Chat history is a bad project tracker. In the Web UI, open an agent and choose the **Deliverables** tab to see root deliverables, scheduled work, and their children in one place. Projects open on their deliverable list first.

Open a deliverable to inspect:

- Its current lifecycle state and readiness.
- **Children** and their rollup (for a composite).
- **Attempts** — each execution episode and the gaps that drove the next one.
- The **Acceptance** ledger — every check result and judgment verdict.
- **Dependencies** and what's still waiting.
- The accepted output, and the session behind any attempt.

When a deliverable is blocked on you, its page shows what it needs — submit your verdict, or waive a failed dependency, right there. The agent picks the work back up with your input.

The practical rule: use chat to describe outcomes and decisions; use deliverables to track execution.
