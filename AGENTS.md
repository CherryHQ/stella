# stella

Stella is a multi-tenant, multi-user, multi-agent AI assistant platform written in Go. It pairs each user with personalized AI agents that have their own memory, tools, schedules, and sandbox policies. Users interact through Telegram, QQ, Feishu, WeChat, the Web UI, or the CLI. The backend is a single `stellad` binary backed by PostgreSQL (an embedded cluster by default, or an external server via `STELLA_DATABASE_URL`).

## Commands

- This project requires `mise` for development workflows.
- On a fresh clone, run `mise run setup` once. Use `mise tasks` to discover workflows.
- Run project workflows through `mise run <task>` instead of invoking underlying tools directly.
- Before committing, **ALWAYS** run: `mise run format && mise run build && mise run test`.
- When touching platform-specific behavior, run a targeted cross-platform build before committing (e.g., `GOOS=windows GOARCH=amd64 go build -o dist/bin/stellad-windows-amd64.exe ./cmd/stellad`).
- Do not run Go tests with `-race` locally by default.
- **Never build the `stellad` binary into the repo root.** Always use `mise run build` (outputs to `dist/bin/`) or specify `-o dist/bin/stellad` explicitly.
- `mise run dev` writes combined UI/API output to `dist/logs/dev.log` and truncates that file on each startup; use it for agent-friendly debugging.

## Development rules

Rules in `web/content/docs/development/rules/` are the **source of truth** for development conventions. Read the relevant rule before designing or changing anything in that domain.

| Domain           | Rule file            | Read before                                                                |
| ---------------- | -------------------- | -------------------------------------------------------------------------- |
| Schema design    | `schema-design.md`   | Designing or changing any table                                            |
| goose migrations | `goose.md`           | Creating or modifying database migrations                                  |
| sqlc queries     | `sqlc.md`            | Writing or editing SQL query files                                         |
| API design       | `api-design.md`      | Designing or changing any HTTP API                                         |
| CLI design       | `cli-design.md`      | Designing or changing any CLI command                                      |
| Web UI           | `web-ui.md`          | Building or reviewing any web UI                                           |
| Web theming      | `web-theming.md`     | Changing the web visual style or tokens                                    |
| Documentation    | `doc-style.md`       | Writing or editing user/developer docs                                     |
| Web UI testing   | `web-ui-test.md`     | Testing the web UI with browser automation                                 |
| Backend API test | `api-test.md`        | Testing the backend via live HTTP API + DB assertions (no browser)         |
| Project tracking | `project-tracker.md` | Managing GitHub issues and project board                                   |
| Release          | `release.md`         | Cutting a release, tagging, changelog                                      |
| Marketing        | `marketing.md`       | Writing a landing page, README opener, hero copy, or any marketing content |

For new or changed HTTP APIs, also follow `api/CLAUDE.md` for the OpenAPI-first workflow.

## Timestamps and timezones

- **Store UTC.** Use `time.Now().UTC()` in Go; schema defaults use `now()`. Never use a local-time conversion.
- **Serialize timezone-aware.** Emit RFC3339 with zone: `t.UTC().Format(time.RFC3339)`. Never return naive `"2006-01-02 15:04:05"` in API responses.
- pgx scans `timestamptz` columns directly into `time.Time`; call `.UTC()` before formatting and serialize with `t.UTC().Format(time.RFC3339)`.
- Convert to user's local zone only at the presentation layer.

## Documentation

Documentation follows the **Diátaxis four-quadrant model** (Tutorial / How-to / Reference / Explanation). Before writing or moving any doc page, decide which quadrant it belongs to and place it in the matching sidebar section. **Read `web/content/docs/development/rules/doc-style.md`** for the full quadrant table, writing conventions, and audience targeting.

When behavior, APIs, config, commands, or architecture change:

- Update `README.md` and/or `web/content/docs/` as appropriate.
- Add new doc pages to the relevant folder's `meta.json` and the matching `sections` entry in `web/content/docs/meta.json`.
- Keep `resources/skills/system/stella/` and `internal/agent/prompt/template/system_prompt.tmpl` in sync with user-facing changes.
- Maintain both English (`*.md`, `*.mdx`) and Chinese (`*.zh.md`, `*.zh.mdx`) versions.
- For CLI usage, command help is the source of truth: put syntax and examples in the relevant `--help` output, and have user docs/skills point to the specific help command (for example, `stella recally save --help`) instead of duplicating full command examples.

## Issue & PR tracking

- **Every PR must link to a GitHub issue.** If a PR is being created without an associated issue, stop and ask the user to create one first (or create it on their behalf). No PR should be opened without a traceable issue.
- When starting a new feature or task, ensure a tracked GitHub issue exists; if not, create one and add it to the project board. **Read `web/content/docs/development/rules/project-tracker.md`** for the full workflow.
- Organize every issue and PR description in four sections: **What**, **Why**, **How**, **Refs**.
- Keep descriptions current as the plan evolves.

## Commit style

Emoji-prefixed Conventional Commits, e.g. `✨ feat:` / `🐛 fix:`. No `Signed-off-by` unless requested.
