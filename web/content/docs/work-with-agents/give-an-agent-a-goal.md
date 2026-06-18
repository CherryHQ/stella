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

## Use tasks for larger goals

If the goal has multiple steps, ask the agent to track it as a goal with a plan:

> Create a goal for this work with a multi-step plan — the steps, their dependencies, and any human review points we need — then run it.

The agent authors a plan and the system materializes it into the child tasks (you cannot hand-attach tasks to a goal). A single-step goal runs immediately; a multi-step goal is planned, then activated. Use chat for context and decisions, and the task UI for execution state. Automatic LLM goal-splitting is not part of this release, so the agent writes the plan explicitly.
