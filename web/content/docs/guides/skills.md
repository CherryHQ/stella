---
title: Skills
---

## What Are Skills

Skills are reusable playbooks that teach Stella how to perform specific tasks. When you ask Stella to do something like "create a GitHub release" or "write a blog post," she can load a skill that gives her step-by-step instructions for that workflow.

Skills are written in plain markdown — they are essentially cheat sheets that Stella reads and follows. You can install skills from public registries, or write your own.

## Skill Scopes

Skills are organized into three scopes:

- **Project skills** — live in your repository under `.agents/skills/`. They ship with the code and are available when the current session is attached to that project.
- **User skills** — personal skills stored in your account for the current agent.
- **Agent skills** — scoped to a specific agent. Useful when different agents need different workflows.

Project skills take precedence over user and agent skills with the same name.

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

### From the CLI

```bash
# Search for skills
stella skill search "git"

# Install from clawhub.ai for the current agent
stella skill install "clawhub:git-helper"

# Install a specific version
stella skill install "clawhub:git-helper@1.2.0"

# Install from GitHub
stella skill install "owner/repo@skill-name"

# Install from a local path
stella skill install "/path/to/skill"
```

## Managing Skills

### From a Conversation

- **"List my installed skills."**
- **"Remove the git-helper skill."**
- **"Load the deployment skill."** — Stella reads the skill's instructions for the current task.

### From the CLI

```bash
# List skills visible to the current agent
stella skill list

# Include project skills from the current project session
stella skill list

# Remove a user skill from the current agent
stella skill remove "git-helper"
```

Skill commands use the scoped `STELLA_TOKEN` injected into Stella agent sessions. The token carries the current agent and session context, and the server verifies that scope on every request.

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

Or from the CLI:

```bash
stella skill create --name "my-skill" --description "What this skill does"
```

The skill body is the markdown content after the frontmatter. Write clear, step-by-step instructions — Stella follows them literally.

## Tips

- **Start by searching.** Before creating a skill from scratch, check if one already exists in the registries.
- **Keep skills focused.** One skill per task. A skill for "deploy" and a skill for "rollback" is better than one skill that tries to do both.
- **Use project skills for team workflows.** Put shared skills in `.agents/skills/` in your repository so everyone on the team benefits.
- **Test skills by loading them.** After creating a skill, ask Stella to load it and try the workflow to verify the instructions work.
