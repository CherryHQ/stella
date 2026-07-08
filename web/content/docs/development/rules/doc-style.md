# Documentation Style

## Diátaxis framework

All documentation follows the [Diátaxis](https://diataxis.fr/) four-quadrant
model. Every page belongs to exactly one quadrant; before writing or moving a
page, decide which quadrant it belongs to and place it in the matching sidebar
section.

| Quadrant        | Purpose                         | Sidebar section    | Path prefix                      |
| --------------- | ------------------------------- | ------------------ | -------------------------------- |
| **Tutorial**    | Learning-oriented walkthrough   | Start Here         | `web/content/docs/start-here/`   |
| **How-to**      | Task-oriented practical guide   | Guides             | `web/content/docs/{feature}/`    |
| **Reference**   | Lookup-oriented facts           | Guides/Development | Feature-specific reference pages |
| **Explanation** | Understanding-oriented concepts | Development        | `web/content/docs/development/`  |

Before writing a new page, decide which quadrant it belongs to:

- **Tutorials** teach by doing — sequential steps that end with a working result. No prerequisites beyond the quickstart.
- **How-to guides** solve a specific task — "How do I schedule an agent?" Assume the reader already understands core concepts.
- **Reference** documents facts — plugin capabilities, platform API, manifest format. Terse, complete, structured for lookup.
- **Explanation** builds understanding — architecture, data flow, design rationale. No steps; discusses _why_.

The sidebar groups directories into these four quadrants via `sections` in
`web/content/docs/meta.json`. When adding a new directory, add it to the
matching section. When adding a new page, add it to the relevant folder's
`meta.json` and, if it creates a new top-level docs area, the matching
`sections` entry in `web/content/docs/meta.json`.

If a doc serves both users and contributors, write a user-facing how-to in the Guides section and a technical deep-dive in Development.

Maintain both English (`*.md`, `*.mdx`) and Chinese (`*.zh.md`, `*.zh.mdx`) versions.

## Writing for users

User-facing docs (`getting-started/`, `guides/`, `channels/`, `README.md`) follow these rules:

- **Lead with what the user can do**, not how it's built. "Schedule Stella to remind you every morning" not "The scheduler uses River with a PostgreSQL-backed job queue."
- **No internal names in user docs.** No database table names (`ctx_messages`, `settings_agents`), no Go types (`PoolManager`, `RunnerFactory`), no library names (`gocron`, `bubblewrap`). Move these to `development/`.
- **No unexplained acronyms.** Spell out on first use or avoid entirely. "LCM (Lossless Context Management)" on first mention, then "LCM" after.
- **Use "you" language.** "You can store API keys in the vault" not "The vault subsystem provides encrypted credential storage."
- **Step-by-step over architecture diagrams.** Users want "do this, then this." Save system diagrams for developer docs.
- **Consistent terminology.** Say "Web UI" not "admin panel." Say `stellad server` not `stella serve`. Say "secret" not "vault entry."
- **Include troubleshooting** in getting-started and channel docs. What if the server won't start? What if Telegram doesn't respond?

## Command documentation

Command help is the source of truth for `stellad` syntax and examples. Put
usage, flags, and examples in the relevant `--help` output, then have user docs
and skills point to the specific help command (for example,
`stellad vault keygen --help`) instead of duplicating command examples.

## Writing for developers

Developer docs (`development/`, `plugins/`) can include internals, Go types, database schemas, and architecture diagrams. Keep them accurate and current, but don't duplicate user-facing guidance — link to the user guide instead.

Codegen configs live in `api/codegen/{types,server,client}.yaml`.
