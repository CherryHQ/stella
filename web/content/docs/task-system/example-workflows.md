---
title: Example Workflows
---

These examples show how to frame larger objectives as deliverables. You describe the root outcome and its acceptance bar; the agent decomposes a composite deliverable into children, drives each to acceptance, and reworks against the gaps until the contract passes.

## Finance reimbursement review

Root deliverable:

> Audit the reimbursement packet for this client dinner and prepare finance review if needed. Accepted when every line is policy-checked, exceptions are flagged, and the packet is ready for finance.

The agent might decompose it into children:

- Extract receipt and attendee data.
- Check policy requirements.
- Identify missing fields.
- Flag policy exceptions.
- Draft the reimbursement packet.
- Route exceptions to a human finance verdict.

Useful dependencies:

- Policy checks consume the accepted receipt extraction.
- Exception review consumes the accepted policy checks.
- The final packet consumes the extraction and the exception review.

Judgment gates:

- Policy exceptions need a human finance verdict.
- Missing tax or receipt details block on user input.
- Payment approval stays with finance staff.

## HR hiring loop

Root deliverable:

> Screen these candidates for the backend role and prepare the hiring panel review. Accepted when each candidate is scored against the rubric and the panel packet is complete.

The agent might decompose it into children:

- Extract resume facts.
- Compare candidates with the role rubric.
- Summarize evidence.
- Identify missing interview signals.
- Prepare panel review packets.
- Route the recommendation to a human panel verdict.

Useful dependencies:

- Rubric comparison consumes the accepted resume facts.
- Evidence summaries consume the accepted rubric comparison.
- Panel packets consume the summaries and the missing-signal check.

Judgment gates:

- Candidate recommendations need a human verdict.
- Sensitive or incomplete evidence is flagged.
- The final hiring decision stays outside the agent.

## Engineering release

Root deliverable:

> Plan the billing workflow release and track every blocker before launch. Accepted when implementation and verification pass and launch is approved.

The agent might decompose it into children:

- Read the release plan.
- Identify affected services.
- Implement the changes.
- Verify the changes.
- Track blockers.
- Route launch approval to a human verdict.

Useful dependencies:

- Implementation consumes the affected-service analysis.
- Verification consumes the accepted implementation — and its checks (build, tests) are the implementation deliverable's own acceptance contract, run automatically and reworked on failure.
- Launch approval consumes the verification results.

Judgment gates:

- Risky migrations need an engineering verdict.
- Customer-visible changes need a product verdict.
- Launch approval stays human.
