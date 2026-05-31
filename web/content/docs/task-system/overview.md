---
title: Task System Overview
---

Stella's task system is for goals that need more than a single chat answer.

When you give an agent a goal, Stella can turn it into a workflow: plan the work, split it into tasks, connect dependencies, define acceptance criteria, track blockers, record events, run agents, and route results through review.

## Goal to plan

The user gives the agent an outcome:

> Prepare the Q2 reimbursement audit packet and flag anything that needs finance review.

The agent should not just answer with advice. It should identify the work needed to reach the outcome.

## Plan to task DAG

Stella can represent the plan as tasks with dependencies:

- Collect reimbursement records.
- Extract receipt metadata.
- Compare each request against policy.
- Flag exceptions.
- Prepare the review packet.
- Ask finance to review exceptions.

Dependencies matter. The review packet should not be marked ready before the policy checks finish. A DAG makes that structure visible.

## Acceptance criteria

Each task should have a clear definition of done. Acceptance criteria make agent work reviewable:

- All required receipt fields are extracted.
- Each reimbursement is marked pass, warning, or needs review.
- Every warning cites the policy rule that triggered it.
- The final packet includes a summary and attachments.

## Review and approval

Some work should stop for a human decision. Stella's task system lets the agent route results through review instead of pretending it has authority.

Use review for:

- Policy exceptions.
- Candidate screening recommendations.
- Release approvals.
- Customer-facing replies.
- Anything that changes money, access, or reputation.

## Task UI

The task UI exists because chat history is a bad project tracker. Use it to inspect:

- Current task status.
- Dependencies.
- Blockers.
- Acceptance criteria.
- Events and runs.
- Review state.

The practical rule: use chat to give goals and context; use the task UI to inspect execution.
