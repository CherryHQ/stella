---
title: Give an Agent a Goal
---

Using Stella should feel like asking a capable coworker for help.

You do not need to know which tool, API, policy document, or workflow the agent will use. Give the agent the goal, relevant context, and any constraints. Stella gives the agent memory, knowledge, skills, tools, and workspace context.

## Write the outcome

Good:

> Prepare a reimbursement packet for this client dinner. Tell me what is missing and create finance review work if there are exceptions.

Weak:

> Help with expenses.

The agent can ask follow-up questions, but a clear outcome saves time.

## Include constraints

Useful constraints:

- Deadline.
- Required format.
- Approval boundary.
- People who should review.
- Source documents to use.
- Things the agent must not do.

## Let the agent use its context

A shared agent brings its own context:

- Instructions from the domain owner.
- Knowledge for the workflow.
- Skills for repeatable methods.
- Tools for external action.
- Memory about you and your preferences.

That is why you can give a normal request instead of manually driving every step.

## Larger goals become tracked Goals

A goal you hand off isn't just a chat reply — it becomes a tracked **Goal** the agent drives to acceptance. For a multi-step objective, say so and state how "done" should look:

> Create a goal for this work, decompose it into the steps and their dependencies, include the human sign-off points we need, then run it. Accepted when the packet is complete and exceptions are approved.

The agent decomposes a composite goal into child goals and runs each to acceptance, reworking against the gaps until the acceptance contract passes. A direct (single-step) goal runs immediately; a decomposed one is planned first, then activated. Completion is **derived** from the contract — the agent never marks it done itself.

Use chat for context and decisions; watch execution on the agent's **Goals** tab. A composite goal detail page opens as a workflow canvas: click the running or blocked node for attempts, readiness, and verdicts; open **Timeline** from the header to leave guidance that authorizes one more retry when the block is not an upstream dependency. See [Goals](/docs/task-system/overview) for the full model.
