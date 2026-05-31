---
title: Example Workflows
---

These examples show how Stella's task system changes a goal into reviewed work.

## Finance reimbursement review

Goal:

> Audit the reimbursement packet for this client dinner and prepare finance review if needed.

Possible tasks:

- Extract receipt and attendee data.
- Check policy requirements.
- Identify missing fields.
- Flag policy exceptions.
- Draft the reimbursement packet.
- Create a finance review task.

Review gates:

- Policy exception requires finance review.
- Missing tax or receipt details require user follow-up.
- Payment approval stays with finance staff.

## HR hiring loop

Goal:

> Screen these candidates for the backend role and prepare the hiring panel review.

Possible tasks:

- Extract resume facts.
- Compare candidates with the role rubric.
- Summarize evidence.
- Identify missing interview signals.
- Prepare panel review packets.
- Schedule review or notify the hiring panel.

Review gates:

- Candidate recommendations require human review.
- Sensitive or incomplete evidence is flagged.
- Final hiring decision stays outside the agent.

## Engineering release

Goal:

> Plan the billing workflow release and track every blocker before launch.

Possible tasks:

- Read the release plan.
- Identify affected services.
- Create implementation and verification tasks.
- Add acceptance criteria.
- Track blockers.
- Prepare release review.

Review gates:

- Risky migrations need engineering review.
- Customer-visible changes need product review.
- Launch approval remains human.
