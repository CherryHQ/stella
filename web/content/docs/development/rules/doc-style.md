# Documentation Style

## Doc structure

Docs are split by audience:

| Section         | Audience                    | Path                                |
| --------------- | --------------------------- | ----------------------------------- |
| Getting Started | Users                       | `web/content/docs/getting-started/` |
| Guides          | Users                       | `web/content/docs/guides/`          |
| Channels        | Users                       | `web/content/docs/channels/`        |
| Development     | Contributors/plugin authors | `web/content/docs/development/`     |

Place new docs in the section matching the target reader. If a doc serves both audiences, write a user-facing version in `guides/` and a technical deep-dive in `development/`.

## Writing for users

User-facing docs (`getting-started/`, `guides/`, `channels/`, `README.md`) follow these rules:

- **Lead with what the user can do**, not how it's built. "Schedule Stella to remind you every morning" not "The scheduler uses gocron/v2 with a SQLite-backed job store."
- **No internal names in user docs.** No database table names (`ctx_messages`, `settings_agents`), no Go types (`PoolManager`, `RunnerFactory`), no library names (`gocron`, `bubblewrap`). Move these to `development/`.
- **No unexplained acronyms.** Spell out on first use or avoid entirely. "LCM (Lossless Context Management)" on first mention, then "LCM" after.
- **Use "you" language.** "You can store API keys in the vault" not "The vault subsystem provides encrypted credential storage."
- **Step-by-step over architecture diagrams.** Users want "do this, then this." Save system diagrams for developer docs.
- **Consistent terminology.** Say "Web UI" not "admin panel." Say `stellad server` not `stella serve`. Say "secret" not "vault entry."
- **Include troubleshooting** in getting-started and channel docs. What if the server won't start? What if Telegram doesn't respond?

## Writing for developers

Developer docs (`development/`, `plugins/`) can include internals, Go types, database schemas, and architecture diagrams. Keep them accurate and current, but don't duplicate user-facing guidance — link to the user guide instead.

Codegen configs live in `api/codegen/{types,server,client}.yaml`.
