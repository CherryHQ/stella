---
name: code-review
description: >
  Review code for correctness, safety, and maintainability. Use when the user asks
  to review a pull request, inspect a patch, audit a change, or evaluate code before merging.
  Triggers on "review this", "check this code", "look at this PR", "is this safe to merge".
tags: [engineering, review]
---

# Code Review

Read the change fully before commenting. Note:

- Bugs — wrong behavior, race conditions, off-by-one, mishandled errors
- Safety — injection, auth bypass, resource leaks, unchecked input at boundaries
- Maintainability — unclear names, dead code, missing tests, over-abstraction
- Style — only if inconsistent with neighboring code, not as a vehicle for opinion

Report findings with file:line references. Severity: critical / warning / nit. Nits are optional.
