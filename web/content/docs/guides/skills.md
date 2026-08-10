---
title: Skills
---

## What Are Skills

Skills are reusable playbooks that teach Stella how to perform specific tasks. When you ask Stella to do something like "create a GitHub release" or "write a blog post," she can load a skill that gives her step-by-step instructions for that workflow.

Skills are written in plain markdown — they are essentially cheat sheets that Stella reads and follows. You can install skills from public registries, or write your own.

## Skill scopes and priority

Stella has two Skill authorities today. Release-provided builtins come only from the immutable, content-addressed release bundle. Project Skills are ordinary files in durable Agent/project working trees. Global, Agent-bound, user, and user-Agent Skills remain stored in PostgreSQL, with execution mirrors derived from them. The later Home filesystem authority cutover has not landed.

The stored scopes are `project`, `user_agent`, `user`, `system_agent`, and `system`. `builtin` is contextual: a release Skill has the immutable identity `builtin:<name>`. An administrator-installed global Skill is the separate mutable identity `system:<name>`, and an Agent-bound administrator Skill is `system_agent:<name>`.

- **Project skills** — live in your repository under `.agents/skills/`. They ship with the code and are available when the current session is attached to that project.
- **User skills** — your personal skills, available across all of your agents.
- **User · this agent** — your personal skills scoped to a single agent.
- **Shared agent skills** — managed by admins and available to everyone who uses that agent.
- **Global skills** — managed by admins and available everywhere. Skills bundled with Stella remain part of the installation; managed global skills can be installed, enabled, disabled, and removed from the Admin Console.

When names collide, Stella selects one winner in this order:

```
project > user · this agent > user > shared agent > global > builtin
```

It applies policy after selecting that winner. Disabling a winner does not reveal a lower-priority Skill with the same name.

## Per-Agent activation

Skills are enabled for an Agent by default. An administrator or durable Agent creator can use that Agent's **Skills** tab to enable or disable a builtin, global, or matching Agent Skill. This is one shared setting: the last committed update wins.

Activation is separate from permission to edit Skill content and from `disable_model_invocation`. A turn already admitted keeps its Skill snapshot; the next turn sees a committed activation change.

Older non-empty activation lists are shown as diagnostics but mean all Skills are enabled. Disabled references to Skills that no longer exist do not affect execution; clear them explicitly in the Web UI.

Before downgrading Stella, re-enable every disabled Skill and clear any dangling disabled references. Older binaries may ignore AgentSkillPolicy v1 and overwrite it during ordinary Agent edits. Do not treat Skill activation in a mixed-version deployment as a security guarantee; it is a product preference, not a filesystem access control.

Manage personal `user` and `user_agent` skills from **Personal Settings → Skills**. Administrators manage deployment-owned `system` and `system_agent` skills from **Admin Console → Deployment resources → Global Skills**. The two pages never mix ownership scopes.

## Installing Skills

### From a Conversation

Ask Stella to find and install skills:

- **"Search for a skill about git releases."**
- **"Install the git-helper skill."**
- **"Find skills related to code review."**

Stella searches across registries and shows you what is available. You can then choose which one to install.

### From Registries

Stella can install skills from several sources:

- **[clawhub.ai](https://clawhub.ai)** — the primary skill registry. Search and install directly from conversation.
- **[skills.sh](https://skills.sh)** — a secondary registry. Results are merged with clawhub.ai searches.
- **GitHub / GitLab** — install any skill hosted in a Git repository.
- **Local paths** — install from a directory on your filesystem.

If you hit rate limits on clawhub.ai, you can set a free API token:

1. Sign up at [clawhub.ai](https://clawhub.ai).
2. Go to Settings, then API Tokens, and create a token.
3. In chat, send: `/config CLAWHUB_TOKEN your-token`

## Managing Skills

### From a Conversation

- **"List my installed skills."**
- **"Remove the git-helper skill."**
- **"Load the deployment skill."** — Stella reads the skill's instructions for the current task.

### From the Web UI

Use Personal Settings to browse, install, and remove your skills. Administrators use Global Skills for deployment-wide and shared-agent skills.

## Creating Your Own Skills

You can create custom skills to teach Stella your workflows. A skill is a directory containing a `SKILL.md` file.

### Skill Format

```markdown
---
name: my-deploy-script
description: Deploy the application to production.
---

# Deploy to Production

Follow these steps to deploy:

1. Run the test suite and confirm all tests pass.
2. Build the production bundle.
3. Push to the production branch.
4. Verify the deployment is healthy.

Always ask the user for confirmation before pushing to production.
```

### Frontmatter Fields

| Field         | Required | Description                               |
| ------------- | -------- | ----------------------------------------- |
| `name`        | Yes      | Lowercase with hyphens, max 64 characters |
| `description` | Yes      | One-line summary shown in search results  |
| `status`      | No       | `draft`, `active` (default), `deprecated` |

### Saving a Custom Skill

You can create skills in conversation:

- **"Create a skill called 'deploy' that describes our deployment process."**

Stella uses the skills tool to create and manage skills directly — no CLI needed.

## Tips

- **Start by searching.** Before creating a skill from scratch, check if one already exists in the registries.
- **Keep skills focused.** One skill per task. A skill for "deploy" and a skill for "rollback" is better than one skill that tries to do both.
- **Use project skills for team workflows.** Put shared skills in `.agents/skills/` in your repository so everyone on the team benefits.
- **Test skills by loading them.** After creating a skill, ask Stella to load it and try the workflow to verify the instructions work.
