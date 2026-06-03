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
- **Never build the `stella` binary into the repo root.** Always use `mise run build` (outputs to `dist/bin/`) or specify `-o dist/bin/stella` explicitly. A binary at the repo root will be committed accidentally — `dist/` is gitignored, the root is not.

## Database migrations

**Before designing or changing any table, read `web/content/docs/development/rules/schema-design.md`** — it is the binding schema-design rule (naming, keys, types, the SQLite/Atlas mapping, and the review checklist).

Schema source of truth: `internal/db/schemas/tables/*.sql`.

Migration workflow:

1. Edit schema files in `internal/db/schemas/tables/`.
2. Run `mise run db:diff -- <name>`.
3. Run `mise run generate`.
4. Commit the generated migration files and `internal/db/migrations/atlas.sum`.

Rules:

- Never hand-write migration SQL.
- Do not edit generated migration checksums manually.

## Timestamps and timezones

- **Store UTC.** Schema timestamp columns default to `(datetime('now'))`, which SQLite evaluates in UTC. Never use `'localtime'`. When writing timestamps from Go, use `time.Now().UTC()`.
- **Serialize timezone-aware.** Always emit RFC3339 with a zone to clients: `t.UTC().Format(time.RFC3339)`. Never return the naive `"2006-01-02 15:04:05"` form in an API response — browsers parse it as local time and render fresh records hours in the past.
- DB strings are naive UTC, so parse them as UTC before reformatting: `time.Parse("2006-01-02 15:04:05", s)` yields UTC; re-emit with `.UTC().Format(time.RFC3339)` (see `parseProjectTime` in `internal/server/projects.go`).
- Keep timestamps as UTC end to end; convert to the user's local zone only at the presentation layer (the web UI renders the local time from the RFC3339 instant).

## API-first design

**Before designing or changing any HTTP API, read `web/content/docs/development/rules/api-design.md`** — it is the binding API-design rule (resource modeling, standard/custom methods, pagination, structured errors, response shape, and the review checklist).

For new or changed HTTP APIs, design from the OpenAPI spec first and follow `api/CLAUDE.md` for the full workflow, generated files, and API-specific rules.

## CLI design

**Before designing or changing any `cmd/stella/` command, read `web/content/docs/development/rules/cli-design.md`** — it is the binding CLI-design rule (command shape, args/flags, help text, stdout/stderr, JSON output, errors, interactivity, and compatibility).

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
- **Consistent terminology.** Say "Web UI" not "admin panel." Say `stella server` not `stella serve`. Say "secret" not "vault entry."
- **Include troubleshooting** in getting-started and channel docs. What if the server won't start? What if Telegram doesn't respond?

### Writing for developers

Developer docs (`development/`, `plugins/`) can include internals, Go types, database schemas, and architecture diagrams. Keep them accurate and current, but don't duplicate user-facing guidance — link to the user guide instead.

Codegen configs live in `api/codegen/{types,server,client}.yaml`.

## Releases

Use `.agents/skills/release/SKILL.md` for the full release workflow.

## Issue & PR tracking

- When you start a new feature or task, make sure it has a tracked GitHub issue: if one already exists, use it; if not, create one and sync it to the Feishu requirements table (see `.agents/skills/project-tracker/SKILL.md`).
- Organize every GitHub issue and PR description in four sections:
  - **What** — the change in one or two sentences.
  - **Why** — the motivation or problem it solves.
  - **How** — the approach, plan, and design details, including notable trade-offs. Capture enough specifics (steps, decisions, alternatives considered) to make the work traceable later.
  - **Refs** — related issues, PRs, docs, or discussions.
- Keep these descriptions current: when the plan or design changes during the work, update the issue/PR so it always reflects the actual approach.

## Commit style

Emoji-prefixed Conventional Commits, for example `✨ feat:` and `🐛 fix:`. Do not add `Signed-off-by` unless explicitly requested.
