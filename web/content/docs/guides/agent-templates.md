---
title: Agent Templates
---

Stella ships with pre-built agent templates so you can create specialized agents in seconds. Each template comes with a recommended model and personality tuned for a specific workflow.

## What are templates?

A template is a starting point for a new agent. Instead of configuring everything from scratch, you pick a template and get a ready-to-use agent with a personality, recommended model, and relevant skills already set up.

After you create an agent from a template, the agent is fully independent. You can customize anything -- the template is just the starting point.

## Available templates

Stella includes these built-in templates:

| Template       | Best for                                                                   |
| -------------- | -------------------------------------------------------------------------- |
| **Default**    | General-purpose assistant -- balanced for everyday conversations and tasks |
| **Coder**      | Software development -- implementation, code review, debugging             |
| **Researcher** | Investigation and analysis -- deep research, fact-checking, comparisons    |
| **Writer**     | Long-form content -- drafting documents, articles, reports                 |

Each template includes a personality (called a "soul") that shapes how the agent communicates. The coder is direct and implementation-focused. The researcher is thorough and methodical. The writer focuses on clarity and structure.

## Creating an agent from a template

1. Open the Web UI.
2. Go to **Agents** and click **Add agent**.
3. You see a grid of available templates plus a "Start from blank" option.
4. Pick a template.
5. The form pre-fills with the template's recommended model and personality.
6. Adjust any fields you want -- name, model, system prompt -- or keep the defaults.
7. Save the agent.

Your new agent is ready to use immediately.

## Customizing agents

Once created, every aspect of an agent is yours to change:

- **Name and description** -- give it a name that makes sense for your workflow.
- **Model** -- switch to a different model or provider at any time.
- **System prompt** -- edit the personality and instructions to match your needs.
- **Skills** -- add or remove skills that the agent can use.

Changes take effect on the next conversation. Existing chat history is preserved.

## Focused sessions

An agent can open a focused session when a bounded task needs fresh context. The focused session keeps its own transcript and returns its reply to the original conversation. The agent can continue it later by session ID.

Session presets provide a standard role, tool set, system instruction, and timeout for this work. Stella currently includes the `coder` preset. You can override it or add presets in your project's `.agents/delegates/` directory. The directory name remains for compatibility; agents start preset runs through the Session tool.
