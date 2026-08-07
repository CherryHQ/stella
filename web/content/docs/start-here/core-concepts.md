---
title: Core Concepts
---

These concepts show up across Stella.

## Tenant

A tenant is the organization boundary. It keeps users, agents, credentials, and data scoped to the right organization.

## User

A user is a person who works with agents. Users can chat from the Web UI, terminal, or connected channels such as Telegram, Discord, QQ, Feishu, and WeChat.

## Agent

An agent is a shared professional partner. It has its own role, instructions, model settings, skills, tools, knowledge, workspace, channel bindings, and memory policy.

Create agents around jobs, not around technical integrations. "Finance reimbursement agent" is better than "spreadsheet bot."

## Session

A session is an ongoing collaboration between a user and an agent. It preserves conversation context and workspace state so work can continue instead of restarting every message.

## Memory

Memory is what lets an agent know a person over time. Stella supports per-user, per-agent memory so the same HR agent can understand Alice and Bob differently.

Stella can also use shared user memory when you want multiple agents to remember the same preferences.

## Knowledge

Knowledge is the agent's professional reference material: policies, process documents, examples, playbooks, PDFs, saved articles, or team-specific context.

Knowledge should answer "what does this agent need to know to do this job well?"

## Skills

Skills are reusable working methods. A skill can teach an agent how to perform a code review, prepare a report, triage an incident, screen a candidate, or follow a finance checklist.

Skills are not just prompts. They package instructions, tool usage patterns, and workflow conventions.

## Tools

Tools are external capabilities the agent can call: command-line tools, APIs, OAuth-connected services, notification channels, file operations, and plugin-provided functions.

Tools answer "what can this agent actually do?"

## Goal

A goal is the outcome the user gives the agent. Good goals describe the desired result, not every implementation step. Stella tracks each goal as a tree of sub-goals with dependencies, blockers, runs, and review states, and works it until the acceptance check passes.

## How work fits together

Two concepts carry work in Stella:

- A **session** is where work happens: context, conversation, and execution, here and now.
- A **goal** is a durable outcome: tracked through acceptance, even across restarts.

Everything else is something you do with a session or a goal, not a third kind of work:

- **Delegating** hands a focused subproblem to an isolated session, so the main conversation stays clean.
- **Remembering** searches across your past sessions, so context is never lost. Memory is also a feature in its own right — you can review and edit what an agent keeps about you in its settings.
- **Workflows** save an accepted goal so you can run it again with new inputs.
- **Schedules** add a time trigger — later, or every morning — to a conversation or a workflow. The schedule is the trigger, not the work.

So when you say "save this and run it every morning", the agent saves the accepted goal as a workflow and schedules it.

## Review

Review is the human checkpoint. The agent can do the work, but the organization can keep judgment, approvals, and accountability where they belong.
