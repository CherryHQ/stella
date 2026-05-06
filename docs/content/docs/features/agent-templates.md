---
title: Agent Templates & Builtin Resources
---

## Overview

Anna ships with a curated catalog of **builtin resources** so a fresh install is useful on day one without hand-editing every prompt. The catalog lives in `plugins/tools/builtin/` and is embedded into the binary at build time.

Four resource kinds are shipped:

| Kind          | Purpose                                                              | Where it runs                                      |
| ------------- | -------------------------------------------------------------------- | -------------------------------------------------- |
| **Skill**     | Reusable knowledge/playbook the agent can load on demand             | DB-synced into `skills(scope='system')` on startup |
| **Soul**      | Persona/tone fragment layered into the agent's system prompt         | Copied into an agent at creation time              |
| **Sub-agent** | Tool-restricted worker preset for delegation via the `agent` tool    | Extracted to `$ANNA_HOME/agents/` on startup       |
| **Template**  | Full agent bootstrap (model + system prompt + soul) | Read once at agent creation; no persistent link    |

## Templates

A template is a complete starting point for a new agent. When you click **Add agent** in the admin UI you see a grid of templates plus a "Start from blank" card. Picking a template pre-fills:

- **Model** — provider/model pair the template recommends
- **System prompt** — copied from the template's referenced soul

User-supplied fields always win. You can edit every field on the form before saving, and after save the agent has no persistent link back to the template — upgrading a template does not touch existing agents.

### Templates that ship today

- `default` — balanced assistant persona
- `coder` — implementation-focused; enables `code-review` and `implementation` skills
- `researcher` — investigation workflows; enables `research` and `task-planning`
- `writer` — longform drafting; enables `docs-writing`

## Souls

Souls are reusable persona fragments. They don't have their own DB row — when you pick one, its content is copied into the new agent's `system_prompt`. After that, the agent owns the text and can diverge freely.

Shipped souls: `default`, `coder`, `researcher`, `direct`, `teacher`.

The `default` soul replaced the previously hardcoded `template/soul.md` — `runner.DefaultAgentSoul()` now resolves it from the registry at runtime.

## Sub-agents

Sub-agent presets describe tool-restricted workers for the `agent` delegation tool (research, review, writing, coding). The runner extracts them into `$ANNA_HOME/agents/` on startup so project-local overrides (`.agents/agents/`) can shadow them with the same filename.

Shipped sub-agents: `coder`, `researcher`, `reviewer`, `writer`.

### Preset front-matter fields

| Field         | Type     | Description                                                                                 |
| ------------- | -------- | ------------------------------------------------------------------------------------------- |
| `name`        | string   | Required. Unique preset identifier used in `agent` tool calls.                              |
| `description` | string   | Required. Human-readable description shown in the preset list.                              |
| `model`       | string   | Optional model override (e.g. `claude-haiku-4-5-20251001`). Defaults to parent model.       |
| `tools`       | string[] | Optional explicit tool allowlist. Omit to inherit all parent tools; use `[]` for no tools.  |
| `timeout`     | duration | Wall-clock deadline for this preset's runs (e.g. `30m`, `1h`). Overrides the global `runner.subagent_timeout`. |

The markdown body below the front-matter becomes the subagent's additional system prompt.

Each subagent run is also bounded by a fixed internal turn limit (50 turns) that is not configurable. Use `timeout` to cap long-running tasks by wall-clock time instead.

## Skills

Every skill synced into the database with `scope='system'` is available to every agent automatically.

An agent's skill catalog in the prompt is:

```
{all system-scope builtin skills}
 ∪ {agent-scope DB skills}
 ∪ {user-scope DB skills}
 ∪ {project skills from .agents/skills}
```


Shipped system skills live under `internal/resources/skills/system/` and are synced into `skills(scope='system')` on startup. Startup sync is authoritative: skills removed from the embedded system catalog are deleted from the database on the next sync.

## Adding a new builtin resource

The catalog is additive — dropping a new file in the right subdir makes it show up everywhere:

```
plugins/tools/builtin/
├── skills/<id>/SKILL.md        # skill (directory with SKILL.md + refs)
├── souls/<id>.md               # soul
├── subagents/<id>.md           # sub-agent preset
└── templates/<id>.md           # template
```

Each file needs YAML frontmatter with at least `id`, `name`, `description`. Templates reference a soul by `soul_id` and skills by a `skills: [names]` array in frontmatter.

On the next build, the admin UI picks up the new resource automatically via the `GET /api/builtin/{kind}` and `GET /api/builtin/{kind}/{id}` endpoints.

## API

Read-only catalog endpoints:

| Endpoint                       | Description                                                             |
| ------------------------------ | ----------------------------------------------------------------------- |
| `GET /api/builtin/{kind}`      | List summaries (no content) for `template`, `soul`, `subagent`, `skill` |
| `GET /api/builtin/{kind}/{id}` | Full resource including body content                                    |

`kind` must be one of `template`, `soul`, `subagent`, `skill`; unknown kinds or IDs return `404`.
