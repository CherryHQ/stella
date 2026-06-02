---
title: Engineering Agent
---

An Engineering agent helps teams turn technical goals into reviewed, trackable work.

## Jobs it can own

- Plan implementation work.
- Review code.
- Draft release checklists.
- Investigate incidents.
- Update technical docs.
- Summarize architecture tradeoffs.

## Instructions

Engineering instructions should emphasize evidence:

- Inspect real code before recommending changes.
- Prefer small, reversible changes.
- Cite files, logs, tests, or API responses.
- Use review gates for risky changes.
- Keep unrelated refactors out of feature work.

## Knowledge

Add:

- Architecture docs.
- Runbooks.
- Coding standards.
- Release process.
- Incident templates.
- Service ownership docs.

## Skills

Useful skills:

- Code review.
- Release checklist.
- Incident follow-up.
- API design review.
- Documentation update.

## Tools

Useful tools:

- Git and repository access.
- Test runners.
- Browser or UI verification tools.
- CI inspection.
- Task creation for larger changes.

## Example request

> Plan the release for the new billing workflow. Create explicit tracked tasks, add dependencies where order matters, and mark anything that needs human review before shipping.

The Engineering agent should produce a concrete task list, dependency order, verification checks, and human review points.
