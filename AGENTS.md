# stella

Stella is a single-tenant, multi-user, multi-agent AI assistant platform written in Go. Each deployment is one tenant and trust boundary. It pairs each user with personalized AI agents that have their own memory, tools, schedules, and sandbox policies. Users interact through Telegram, QQ, Feishu, WeChat, or the Web UI. The backend is a single `stellad` binary backed by PostgreSQL (an embedded cluster by default, or an external server via `STELLA_DATABASE_URL`).

## Layout

- `cmd/stellad/` is the single server binary entry point and owns operator commands, service management, and startup wiring.
- `internal/` is the Go backend (~36 packages). Agents usually need `agent` (runtime/sandbox/tools), `server` (HTTP API), `auth`/`authz`, `db` (migrations and sqlc queries), `scheduler`, and `goal`.
- `internal/cli/` is shared command-output/env plumbing for `stellad`, not a user-facing chat CLI.
- `api/` contains the OpenAPI spec and generated contracts; follow `api/CLAUDE.md` for spec-first API changes.
- `web/` contains the frontend and docs content, including these development rules.
- `resources/skills/` contains built-in system, channel, provider, sandbox, hook, and tool skills.
- `plugins/` contains built-in plugin packages; `pkg/` contains reusable public Go packages.

## Commands

- This project requires `mise` for development workflows.
- On a fresh clone, run `mise run setup` once. Use `mise tasks` to discover workflows.
- Run project workflows through `mise run <task>` instead of invoking underlying tools directly.
- Before committing, **ALWAYS** run: `mise run format && mise run build && mise run test`.
- `mise run system-test` runs the subprocess system suite (real `stellad` over TCP against embedded PostgreSQL); it is a local and tag-release gate and requires a supported runtime host. `mise run release:validate` runs the full local pre-release gate sequentially (format → build → test → system-test → release checks).
- When touching platform-specific behavior, run a targeted cross-platform build before committing (e.g., `GOOS=windows GOARCH=amd64 go build -o dist/bin/stellad-windows-amd64.exe ./cmd/stellad`).
- Do not run Go tests with `-race` locally by default.
- Build with `mise run build` (outputs to `dist/bin/`) or specify `-o dist/bin/stellad` explicitly; never build the `stellad` binary into the repo root.
- `mise run dev` writes combined UI/API output to `dist/logs/dev.log` and truncates that file on each startup; use it for agent-friendly debugging.

## Goose migrations

- Create migrations only with `mise run db:migrate:new -- <name>`; after
  `90000000000000_sequential_versioning.sql`, versions are sequential and
  contiguous. Never run `goose fix`.

## Development rules

Rules in `web/content/docs/development/rules/` are the **source of truth** for development conventions. Read the relevant rule before designing or changing anything in that domain.

| Domain            | Rule file            | Read before                                                                 |
| ----------------- | -------------------- | --------------------------------------------------------------------------- |
| Schema design     | `schema-design.md`   | Designing or changing any table                                             |
| goose migrations  | `goose.md`           | Creating or modifying database migrations                                   |
| sqlc queries      | `sqlc.md`            | Writing or editing SQL query files                                          |
| API design        | `api-design.md`      | Designing or changing any HTTP API                                          |
| Go patterns       | `go-patterns.md`     | Writing or reviewing Go concurrency, secret-redaction, or file-install code |
| CLI design        | `cli-design.md`      | Designing or changing any `stellad` operator command                        |
| Web UI            | `web-ui.md`          | Building or reviewing any web UI                                            |
| Web theming       | `web-theming.md`     | Changing the web visual style or tokens                                     |
| Current web theme | `web-design.md`      | Styling against the current theme or consulting the visual direction        |
| Documentation     | `doc-style.md`       | Writing or editing user/developer docs                                      |
| Web UI testing    | `web-ui-test.md`     | Testing the web UI with browser automation                                  |
| Web perf testing  | `web-perf-test.md`   | Measuring or optimizing web UI performance                                  |
| Backend API test  | `api-test.md`        | Testing the backend via live HTTP API + DB assertions (no browser)          |
| System test       | `system-test.md`     | Adding or running the subprocess system suite; choosing a test layer        |
| Project tracking  | `project-tracker.md` | Managing Feishu plans, GitHub issues, and pull requests                     |
| Release           | `release.md`         | Cutting a release, tagging, changelog                                       |
| Marketing         | `marketing.md`       | Writing a landing page, README opener, hero copy, or any marketing content  |

For new or changed HTTP APIs, also follow `api/CLAUDE.md` for the OpenAPI-first workflow.

Test at the lowest sufficient layer: add a subprocess system-test journey only for a seam a Go test cannot reach (process startup, real HTTP auth, SSE transport, cross-request flows, async workers); everything else stays an in-process test. See `system-test.md`.

## Timestamps and timezones

- **Store UTC and serialize timezone-aware.** Use `time.Now().UTC()` in Go; schema defaults use `now()`. Emit RFC3339 with zone via `t.UTC().Format(time.RFC3339)`. Never return naive local time in code or API responses. pgx scans `timestamptz` into `time.Time`; call `.UTC()` before formatting.
- Convert to the user's local zone only at the presentation layer.

## Documentation

Read `web/content/docs/development/rules/doc-style.md` before writing or editing any doc.

When behavior, APIs, config, commands, or architecture change:

- Update `README.md` and/or `web/content/docs/` as appropriate.
- Keep `resources/skills/system/stella/` and `internal/agent/prompt/template/system_prompt.tmpl` in sync with user-facing changes.
- Maintain both English (`*.md`, `*.mdx`) and Chinese (`*.zh.md`, `*.zh.mdx`) versions.

## Issue & PR tracking

- **Every PR must link to a GitHub issue.** If a PR is being created without an associated issue, stop and ask the user to create one first (or create it on their behalf). No PR should be opened without a traceable issue.
- When starting a new feature or task, ensure a tracked GitHub issue exists. Maintainer-committed work must also be linked from a Feishu Task; community issues remain GitHub-only until accepted and scheduled. **Read `web/content/docs/development/rules/project-tracker.md`** for the full workflow.

## Commit style

Emoji-prefixed Conventional Commits, e.g. `✨ feat:` / `🐛 fix:`. No `Signed-off-by` unless requested.
