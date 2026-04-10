# anna

## Core rules

- Prefer small, single-purpose files and shared helpers over duplicated logic.
- Make small, complete, reviewable changes.
- Do not guess. If required intent is ambiguous, ask.
- Always propagate errors and remove dead code or stale comments.

## Commands

- Use `mise tasks` to discover available tasks.
- Always run project workflows through `mise run <task>` instead of calling tools directly.

## Plugins

- Before changing plugins, read `docs/content/docs/plugins/`.
- Treat plugin docs as the source of truth.
- Verify plugin changes with `mise run test`.

## Database migrations

Schema source of truth: `internal/db/schemas/tables/*.sql`.

Workflow:
1. Edit schema files.
2. Run `mise run db:diff -- <name>`.
3. Run `mise run generate`.
4. Commit generated migrations with `internal/db/migrations/atlas.sum`.

- Never hand-write migration SQL.

## Testing and quality

- Do not run Go tests with `-race` locally by default.
- Use `mise run test` and `mise run test:coverage` locally.
- Reserve race-enabled runs for CI.
- Before committing, run `mise run format` and `mise run lint`.
- Use emoji-prefixed conventional commits such as `✨ feat:` and `🐛 fix:`.

## Documentation

When behavior, APIs, config, commands, or architecture change:

- Update `README.md` and/or `docs/content/docs/` as needed.
- Keep `README.md` concise; put detailed content in `docs/content/docs/`.
- Add new docs to the appropriate folder `meta.json` when needed.
- Keep `internal/agent/runner/builtin/anna/` in sync with user-facing changes.

## Releases

Use `.agents/skills/release/SKILL.md` for the full release workflow.

Quick reference:
- `mise run release:check`
- `mise run release:snapshot`
- `mise run release`

Tag format: `vX.Y.Z`.
