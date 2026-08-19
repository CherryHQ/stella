---
title: Core Concepts
---

These concepts show up across Stella.

## Tenant

A tenant is the organization boundary. It keeps users, agents, credentials, and data scoped to the right organization.

## User

A user is a person who works with agents. Users can chat from the Web UI, terminal, or connected channels such as Telegram, Discord, QQ, Feishu, DingTalk, and WeChat.

## Agent

An agent is a shared professional partner. It has its own role, instructions, model settings, skills, tools, knowledge, workspace, channel bindings, and memory policy.

Create agents around jobs, not around technical integrations. "Finance reimbursement agent" is better than "spreadsheet bot."

## Session

A session is an ongoing collaboration between a user and an agent. It preserves conversation context and workspace state so work can continue instead of restarting every message. An agent can search its sessions, inspect a bounded transcript, open a focused session, and continue an existing session.

Agent messages sent from one session to another keep a source label in the transcript. Stella treats that input as information from the sending agent, not as a human instruction. If the target session is busy, agent sends wait in arrival order instead of running concurrently.

## Memory

Memory is what lets an agent know a person over time. Stella supports per-user, per-agent memory so the same HR agent can understand Alice and Bob differently.

Stella can also use shared user memory when you want multiple agents to remember the same preferences.

## Library

The Library is the agent's professional reference material: policies, process documents, examples, playbooks, or team-specific context that a user or administrator intentionally uploads.

You can upload supported documents, spreadsheets, presentations, web pages, ebooks, and text or data files up to 25 MiB. PDF extraction currently supports pages that already contain selectable text; scanned pages require optical character recognition before upload.

The Library should answer "what does this agent need to know to do this job well?" It is searched when a conversation needs evidence; ordinary chat attachments do not enter it automatically.

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

- **Focused work** opens an isolated session for a bounded subproblem, so the main conversation stays clean.
- **Remembering** searches across past sessions through the Session tool. Memory stores durable profile, preference, constraint, and knowledge facts that you can review in settings.
- **Workflows** save an accepted goal so you can run it again with new inputs.
- **Schedules** add a time trigger — later, or every morning — to a conversation or a workflow. The schedule is the trigger, not the work.

So when you say "save this and run it every morning", the agent saves the accepted goal as a workflow and schedules it.

The Web UI follows the same split. Each agent has two spaces:

- **Conversations** — the thread list in the sidebar, and the page behind its title: every thread you have with that agent. Each thread uses a colored icon for its latest state: working, succeeded, or failed. Opening a completed thread marks its result read and returns the icon to idle.
- **Work** — everything that agent is tracking to an outcome, in the order you need it: what **needs you**, what is **active**, what is **scheduled**, what is **repeatable** (your saved workflows), and the **history**.

**Inbox** is the same "needs you", one scope wider: it collects what is waiting on you across every agent, so you never have to check them one by one.

## Review

Review is the human checkpoint. The agent can do the work, but the organization can keep judgment, approvals, and accountability where they belong.
