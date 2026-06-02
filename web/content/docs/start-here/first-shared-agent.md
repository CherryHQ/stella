---
title: First Shared Agent
---

This walkthrough uses a Finance agent because the shape is easy to recognize: many people need help, the workflow is professional, and the work needs consistent rules.

## 1. Choose the job

Start with one job the agent should own.

Good:

- Help employees prepare reimbursement requests.
- Check whether receipts match policy.
- Explain reimbursement rules in plain language.
- Prepare a review packet for finance staff.

Too broad:

- Do finance.
- Manage the company.

## 2. Write instructions

Instructions define how the agent should behave.

For a Finance agent, include:

- The agent's role.
- The reimbursement policies it should follow.
- What information it should ask from employees.
- When it should escalate to finance staff.
- What it must never approve by itself.

## 3. Add knowledge

Add the documents the agent needs to answer correctly:

- Reimbursement policy.
- Receipt requirements.
- Travel rules.
- Approval thresholds.
- Examples of accepted and rejected requests.

Knowledge keeps the agent grounded in your organization's rules instead of generic finance advice.

## 4. Add skills and tools

Add skills for repeatable work:

- Check reimbursement completeness.
- Summarize an expense packet.
- Draft a finance review note.
- Ask for missing documents.

Add tools only when the agent needs to act outside chat:

- Read uploaded files.
- Notify the finance team.
- Call an internal reimbursement API.
- Create a task for manual review.

## 5. Share the agent

Make the agent available to the right users or channels. A shared agent should have one professional identity, but it can remember each user separately.

Employees can now ask:

> I need to submit a reimbursement for a client dinner last Friday. Here are the receipt and attendee list. Tell me what is missing and prepare the packet.

The agent can use its instructions, knowledge, skills, tools, and memory to move the request forward.

## 6. Add task tracking when the work grows

If a goal needs multiple steps, use Stella's task system. Create a goal, add explicit child tasks, wire dependencies where order matters, and use human review for decisions that require accountability.
