---
title: Skills
---

## Overview

Skills are reusable playbooks — markdown files that tell the agent how to perform a specific task. They are loaded on demand during a conversation and can be installed from external registries or created locally.

Stella supports three skill scopes:

| Scope       | Location                         | Who can write   |
| ----------- | -------------------------------- | --------------- |
| **project** | `{PROJECT_ROOT}/.agents/skills/` | Git (read-only) |
| **user**    | DB, encrypted per user           | User            |
| **agent**   | DB, scoped to a specific agent   | User            |

Project skills ship with the repository and take precedence over user/agent skills of the same name.

## Registries

### clawhub.ai

[clawhub.ai](https://clawhub.ai) is the primary skill registry. Search and install skills directly from a conversation:

```
skills action=search query=<term>
skills action=install source="clawhub:<slug>"
skills action=install source="clawhub:<slug>@<version>"
```

**Rate limits.** Anonymous access has a low request quota. If you hit a 429 error, set a free API token:

1. Sign up or log in at [https://clawhub.ai](https://clawhub.ai)
2. Go to **Settings → API Tokens** → create a token → copy it
3. Send in chat:
   ```
   /config CLAWHUB_TOKEN <your-token>
   ```

Stella also automatically falls back to the CN mirror (`cn.clawhub-mirror.com`) on 429, so most users never need a token.

**Environment variables**

| Variable        | Purpose                                              |
| --------------- | ---------------------------------------------------- |
| `CLAWHUB_TOKEN` | Bearer token for authenticated access                |
| `CLAWHUB_URL`   | Override the registry base URL (default: clawhub.ai) |

### skills.sh

[skills.sh](https://skills.sh) is a secondary registry. Results are merged with clawhub.ai results in every `search` call. Install format:

```
skills action=install source="owner/repo@skill-name"
```

### GitHub / GitLab

Install any skill hosted in a Git repository:

```
skills action=install source="owner/repo@skill-name"
skills action=install source="https://github.com/owner/repo/tree/main/path/to/skill"
```

### Local path

```
skills action=install source="/path/to/skill"
skills action=install source="./relative/path"
```

## Managing skills

| Action    | Example                                              |
| --------- | ---------------------------------------------------- |
| Search    | `skills action=search query=git`                     |
| Install   | `skills action=install source="clawhub:git-helper"`  |
| List      | `skills action=list`                                 |
| Load      | `skills action=load name=git-helper`                 |
| Remove    | `skills action=remove name=git-helper`               |
| Create    | `skills action=create name=my-skill description=...` |
| Patch     | `skills action=patch name=my-skill status=active`    |
| Deprecate | `skills action=deprecate name=my-skill`              |

Use `scope=agent` on install/remove/create to target the current agent scope instead of the default user scope.

## Skill format

A skill is a directory containing at minimum a `SKILL.md` file with a YAML frontmatter header:

```markdown
---
name: my-skill
description: One-line description shown in search results.
status: active
---

# My Skill

Instructions for the agent go here.
```

**Frontmatter fields**

| Field                      | Required | Description                                                    |
| -------------------------- | -------- | -------------------------------------------------------------- |
| `name`                     | Yes      | Lowercase, hyphens only, max 64 chars                          |
| `description`              | Yes      | Shown in `list` and `search` output                            |
| `status`                   | No       | `draft` / `active` (default) / `deprecated`                    |
| `disable-model-invocation` | No       | If `true`, skill is injected but model cannot call it directly |
