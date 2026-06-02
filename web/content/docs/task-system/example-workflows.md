---
title: Example Workflows
---

These examples show how to model larger goals as explicit tasks. Stella does not auto-plan the child tasks yet; create the tasks yourself or ask an agent to create them through the task commands.

## Finance reimbursement review

Goal:

> Audit the reimbursement packet for this client dinner and prepare finance review if needed.

Create child tasks:

- Extract receipt and attendee data.
- Check policy requirements.
- Identify missing fields.
- Flag policy exceptions.
- Draft the reimbursement packet.
- Create a human finance review task for exceptions.

Useful dependencies:

- Policy checks depend on receipt extraction.
- Exception review depends on policy checks.
- The final packet depends on extraction and exception review.

Review gates:

- Policy exceptions require human finance review.
- Missing tax or receipt details require user follow-up.
- Payment approval stays with finance staff.

## HR hiring loop

Goal:

> Screen these candidates for the backend role and prepare the hiring panel review.

Create child tasks:

- Extract resume facts.
- Compare candidates with the role rubric.
- Summarize evidence.
- Identify missing interview signals.
- Prepare panel review packets.
- Notify the hiring panel for human review.

Useful dependencies:

- Rubric comparison depends on extracted resume facts.
- Evidence summaries depend on rubric comparison.
- Panel packets depend on summaries and missing-signal checks.

Review gates:

- Candidate recommendations require human review.
- Sensitive or incomplete evidence is flagged.
- Final hiring decision stays outside the agent.

## Engineering release

Goal:

> Plan the billing workflow release and track every blocker before launch.

Create child tasks:

- Read the release plan.
- Identify affected services.
- Create implementation tasks.
- Create verification tasks.
- Track blockers.
- Prepare release review.

Useful dependencies:

- Implementation tasks depend on affected-service analysis.
- Verification tasks depend on implementation tasks.
- Release review depends on verification results.

Review gates:

- Risky migrations need engineering review.
- Customer-visible changes need product review.
- Launch approval remains human.
