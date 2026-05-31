---
title: Create a Professional Agent
---

A professional agent is created by the person or team that owns the workflow.

Finance should create the Finance agent. HR should create the HR agent. Engineering should create the Engineering agent. The point is not that everyone becomes an automation builder. The point is that domain owners encode how their work should be done once, then share that agent with everyone who needs it.

## What to configure

### Instructions

Instructions define the agent's job, tone, boundaries, escalation rules, and decision policy.

Good instructions answer:

- What work does this agent own?
- What questions should it ask before acting?
- What rules must it follow?
- What decisions can it make?
- What decisions require review?

### Knowledge

Knowledge gives the agent professional context:

- Policies.
- Process documents.
- Examples.
- Checklists.
- Product docs.
- Internal references.

Knowledge should be maintained by the domain owner, not guessed by the model.

### Skills

Skills teach repeatable methods:

- Screen a candidate.
- Check a reimbursement packet.
- Prepare a release checklist.
- Summarize a policy change.
- Draft a review brief.

Use skills when the same workflow should be performed consistently.

### Tools

Tools let the agent act:

- Read files.
- Search sources.
- Call APIs.
- Send notifications.
- Use OAuth-connected services.
- Create tasks for tracked work.

Do not add tools just because they exist. Add tools that the agent needs to complete its job.

### Memory rules

Decide what the agent should remember:

- Per-user preferences.
- Past decisions.
- Team-specific vocabulary.
- Communication style.
- Shared user memory that multiple agents can use.

Memory should make work smoother without leaking context across the wrong boundary.

## Share the agent

After configuration, make the agent available to the right users and channels. A shared agent should feel like a specialist coworker: same professional identity for everyone, personalized understanding for each user.
