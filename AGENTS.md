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

- Use `mise tasks` to discover available workflows.
- Run project workflows through `mise run <task>` instead of invoking underlying tools directly.
- Before committing, always run:
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

- Update `README.md` and/or `docs/content/docs/` as appropriate.
- Keep `README.md` concise; put detailed explanations in `docs/content/docs/`.
- Add new documentation pages to the relevant folder `meta.json`.
- Keep `internal/agent/runner/builtin/stella/` synchronized with user-facing changes.
- Documentation is maintained only in English and Chinese:
  - English: `*.md`, `*.mdx`
  - Chinese: `*.zh.md`, `*.zh.mdx`
- Always update both English and Chinese docs when adding or changing documentation content.

Codegen configs live in `api/codegen/{types,server,client}.yaml`.

## Releases

Use `.agents/skills/release/SKILL.md` for the full release workflow.

## Commit style

Emoji-prefixed Conventional Commits, for example `✨ feat:` and `🐛 fix:`. Do not add `Signed-off-by` unless explicitly requested.
