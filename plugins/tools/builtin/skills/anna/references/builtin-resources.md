# Builtin resources reference

Anna ships a curated catalog of builtin resources (souls, sub-agents, templates, skills) embedded in the binary. The admin UI reads the catalog to seed new agents; the runtime reads it to resolve the default soul and extract sub-agent presets.

## Kinds

| Kind | Storage | When it's used |
|------|---------|----------------|
| Skill | Synced to `skills(scope='system')` on startup | Listed in the system prompt catalog. Always-on: `anna`. Others must be opted in per agent. |
| Soul | Embed-only | Copied into the new agent's `system_prompt` at creation. After save, the agent owns the text. |
| Sub-agent | Extracted to `$ANNA_HOME/agents/` on startup | Loaded by the `agent` delegation tool. Project-local `.agents/agents/` shadows by filename. |
| Template | Embed-only | Read once on the admin UI's "Create agent" form to pre-fill model + soul + enabled skills. |

## Per-agent enabled builtin skills

`settings_agents.enabled_builtin_skills` is a JSON array of skill names. An agent's prompt catalog resolves as:

```
{always-on: anna}
 ∪ {enabled_builtin_skills}
 ∪ {agent-scope DB skills}
 ∪ {user-scope DB skills}
```

`anna` is always included; every other system-scope skill must appear in `enabled_builtin_skills` to show up in the prompt. Templates set the list for you; you can toggle chips on the agent form to override.

## Admin API

| Endpoint | Returns |
|----------|---------|
| `GET /api/builtin/{kind}` | List of summaries (no content) for `template`, `soul`, `subagent`, `skill` |
| `GET /api/builtin/{kind}/{id}` | Full resource including body content |

Unknown `kind` or `id` → `404`.

## Adding a new resource

Drop a markdown file in the matching subdir under `plugins/tools/builtin/`:

```
skills/<id>/SKILL.md        # skill (directory; id = dir name)
souls/<id>.md               # soul
subagents/<id>.md           # sub-agent preset
templates/<id>.md           # template
```

Frontmatter requires `id`, `name`, `description`. Templates add `model`, `soul_id`, and `skills: [...]`. On the next build the registry picks it up and the admin UI lists it automatically.
