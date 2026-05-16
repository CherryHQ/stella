# stella

You are working on **stella**, a Go CLI/service project. Act as an engineering collaborator: make small, complete, reviewable changes; protect user data; and preserve unrelated work.

## Working principles

- Prefer boring, reversible solutions over clever ones.
- Prefer small, single-purpose files and shared helpers over duplicated logic.
- Do not guess. If intent, requirements, or ownership is ambiguous, ask before changing behavior.
- Propagate errors; do not swallow failures silently.
- Remove dead code and stale comments when touching nearby code.
- Keep changes focused. Do not mix refactors with feature or bug-fix work unless required.

## Commands

- This project requires `mise` for development workflows.
- On a fresh clone, run `mise run setup` once to set up the development environment and pre-commit hooks.
- Use `mise tasks` to discover available workflows.
- Run project workflows through `mise run <task>` instead of invoking underlying tools directly.
- Before committing, **ALWAYS** run:
  - `mise run format`
  - `mise run build`
  - `mise run test`
- Do not run Go tests with `-race` locally by default.

## Database migrations

Schema source of truth: `internal/db/schemas/tables/*.sql`.

Migration workflow:

1. Edit schema files in `internal/db/schemas/tables/`.
2. Run `mise run db:diff -- <name>`.
3. Run `mise run generate`.
4. Commit the generated migration files and `internal/db/migrations/atlas.sum`.

Rules:

- Never hand-write migration SQL.
- Do not edit generated migration checksums manually.

## API-first design

For new or changed HTTP APIs, design from the OpenAPI spec first and follow `api/CLAUDE.md` for the full workflow, generated files, and API-specific rules.

## Documentation

When behavior, APIs, config, commands, or architecture change:

- Update `README.md` and/or `web/content/docs/` as appropriate.
- Keep `README.md` concise; put detailed explanations in `web/content/docs/`.
- Add new documentation pages to the relevant folder `meta.json`.
- Keep `resources/skills/system/stella/` synchronized with user-facing changes.
- Keep `internal/agent/prompt/template/system_prompt.tmpl` CLI descriptions in sync with actual command `Usage` strings.
- Documentation is maintained only in English and Chinese:
  - English: `*.md`, `*.mdx`
  - Chinese: `*.zh.md`, `*.zh.mdx`
- Always update both English and Chinese docs when adding or changing documentation content.

### Doc structure

Docs are split by audience:

| Section         | Audience                    | Path                                |
| --------------- | --------------------------- | ----------------------------------- |
| Getting Started | Users                       | `web/content/docs/getting-started/` |
| Guides          | Users                       | `web/content/docs/guides/`          |
| Channels        | Users                       | `web/content/docs/channels/`        |
| Development     | Contributors/plugin authors | `web/content/docs/development/`     |

Place new docs in the section matching the target reader. If a doc serves both audiences, write a user-facing version in `guides/` and a technical deep-dive in `development/`.

### Writing for users

User-facing docs (`getting-started/`, `guides/`, `channels/`, `README.md`) follow these rules:

- **Lead with what the user can do**, not how it's built. "Schedule Stella to remind you every morning" not "The scheduler uses gocron/v2 with a SQLite-backed job store."
- **No internal names in user docs.** No database table names (`ctx_messages`, `settings_agents`), no Go types (`PoolManager`, `RunnerFactory`), no library names (`gocron`, `bubblewrap`). Move these to `development/`.
- **No unexplained acronyms.** Spell out on first use or avoid entirely. "LCM (Lossless Context Management)" on first mention, then "LCM" after.
- **Use "you" language.** "You can store API keys in the vault" not "The vault subsystem provides encrypted credential storage."
- **Step-by-step over architecture diagrams.** Users want "do this, then this." Save system diagrams for developer docs.
- **Consistent terminology.** Say "admin panel" not "web UI." Say `stella server` not `stella serve`. Say "secret" not "vault entry."
- **Include troubleshooting** in getting-started and channel docs. What if the server won't start? What if Telegram doesn't respond?

### Writing for developers

Developer docs (`development/`, `plugins/`) can include internals, Go types, database schemas, and architecture diagrams. Keep them accurate and current, but don't duplicate user-facing guidance — link to the user guide instead.

Codegen configs live in `api/codegen/{types,server,client}.yaml`.

## Releases

Use `.agents/skills/release/SKILL.md` for the full release workflow.

## Commit style

Emoji-prefixed Conventional Commits, for example `✨ feat:` and `🐛 fix:`. Do not add `Signed-off-by` unless explicitly requested.
