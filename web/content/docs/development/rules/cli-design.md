---
title: CLI design rules
description: Command-line interface conventions for Stella's cmd/stella commands.
---

> This is a **rule file** for contributors. When you add or change a command
> under `cmd/stella/`, read this page first and follow it. Stella follows the
> spirit of the [Command Line Interface Guidelines](https://github.com/cli-guidelines/cli-guidelines):
> make commands predictable, scriptable, composable, and kind to humans using a
> terminal at 2 a.m.

Stella's CLI is a first-party human/operator client for `stellad server`, not a second backend and not an agent integration surface. For state-changing features, read [CLI and native agent tools](../cli-as-client) and
[API design rules](./api-design) before designing the command surface.

## Core Principles

1. **Human-friendly by default, machine-friendly on request.** Default output
   should be readable in a terminal. Structured output belongs behind `--json`.
2. **Boring beats clever.** Commands should do exactly what their names imply.
   Avoid hidden behavior, implicit network writes, and surprising defaults.
3. **Composability matters.** Commands should work in scripts: stable exit
   codes, stderr for diagnostics, stdout for data, no unsolicited prompts in
   non-interactive contexts.
4. **One command, one job.** A subcommand should have a clear purpose. If it
   needs paragraphs to explain what it does, split it or rename it.
5. **Preserve user data.** Destructive commands need explicit intent and clear
   error messages. Never silently delete, overwrite, or revoke.

## Command Shape

Use the project pattern:

```text
stella <noun> <verb> [args] [flags]
```

Examples:

```text
stella recally save <url>
stella recally feed add <url>
stella task cancel <task-id>
stella share artifact <path>
```

### Naming

- Top-level commands are domain nouns: `recally`, `task`, `skill`, `vault`.
- Subcommands are verbs or resource nouns when another level is needed:
  `list`, `get`, `create`, `cancel`, `feed add`.
- Use lowercase, hyphen-separated names for multi-word commands and flags.
- Prefer common verbs consistently:

| Action              | Use                                      | Avoid                                     |
| ------------------- | ---------------------------------------- | ----------------------------------------- |
| Show many resources | `list`                                   | `ls`, `all`, `show-all`                   |
| Show one resource   | `get` or `read`                          | `show`, `info` unless already established |
| Create a resource   | `create` or domain-specific `add`/`save` | `new` mixed randomly with `create`        |
| Change a resource   | `update`                                 | `modify`, `edit` unless interactive       |
| Remove a resource   | `delete` or domain-specific `remove`     | `destroy`, `rm`                           |
| Stop running work   | `cancel`                                 | `kill`, `abort`                           |

Keep existing command names stable. If a rename is worth it, keep the old name
as an alias for at least one release unless the old behavior is actively unsafe.

### Arguments vs flags

Use positional arguments for the primary object the command acts on:

```text
stella task get <task-id>
stella share artifact <path>
```

Use flags for modifiers, optional context, filters, and output controls:

```text
stella task list --status open --json
```

Rules:

- Required positional args must appear in `ArgsUsage`.
- Required flags are allowed only when there is no natural positional form.
- Boolean flags should be positive (`--follow`, `--json`, `--force`). Avoid
  negative flags like `--no-cache` unless disabling a default is the feature.
- Reuse flag names across commands: `--server-url`, `--json`, `--force`, `--limit`.
- Do not design sandbox-facing CLI commands. Agent capabilities must ship as native tools with server-side identity, not CLI flags or scoped bearer tokens.

## Help Text

Every command should be understandable from `stella help <command>`.

In `urfave/cli` terms:

- `Name`: short, lowercase command token.
- `Usage`: one sentence, imperative or noun-phrase, no trailing period.
- `Description`: only when the command needs context, examples, or warnings.
- `ArgsUsage`: include every positional argument, e.g. `<task-id>`.
- `Category`: set on top-level commands (`Feature`, `System`, `Admin`) when it
  helps the main help screen.

Good:

```go
Usage:    "Create public share links",
ArgsUsage: "<path>",
```

Bad:

```go
Usage: "Does stuff with the thing",
```

Help text is user-facing documentation. If command names, usage strings, or
important flags change, update user docs. Agent-facing prompts and skills should
point to native tools, not CLI command syntax.

## Output

### stdout is for data

Anything a user may pipe to another program goes to stdout:

```bash
stella task list --json | jq '.tasks[].id'
```

Human-readable tables and successful result summaries may also go to stdout.
Keep them stable enough that users are not punished for light scripting, but do
not promise table formats as an API.

### stderr is for diagnostics

Progress, warnings, prompts, and errors go to stderr. This keeps stdout clean
for pipes and command substitution.

```text
Downloading attachments...        # stderr
{"id":"...","status":"done"}    # stdout
```

### JSON output

Any command expected to be used from scripts should support `--json` unless its
only output is already a raw scalar or file content.

Rules:

- Emit valid JSON to stdout and nothing else on stdout.
- Use the same `snake_case` field names as the API.
- Prefer the API response shape directly; do not invent a second CLI schema.
- Pretty-printing is fine for human use, but avoid changing the structure.
- Errors still go to stderr and use non-zero exit codes; do not print successful
  JSON envelopes for failed commands.

### Tables

For human list output, use aligned columns with concise headers. Avoid wrapping
large text in tables; put long content in `read`, `get`, or `--json` output.

Use stable identifiers in the first column when possible:

```text
ID        STATUS    TITLE
abc123    open      Fix scheduler retry
```

## Errors and Exit Codes

Errors should be brief, specific, and actionable:

```text
missing bearer token: set STELLA_TOKEN or sign in through the Web UI
```

Bad errors leak implementation or force guesswork:

```text
sql: no rows in result set
invalid input
```

Exit code rules:

| Code | Meaning                                                             |
| ---- | ------------------------------------------------------------------- |
| 0    | Success                                                             |
| 1    | Expected failure: validation, not found, server error, auth failure |
| 2    | Command-line usage error when the CLI framework distinguishes it    |

Do not add a complex exit-code taxonomy unless a real automation use case needs
it. One bit of failure is enough most of the time.

When wrapping errors in Go, add the operation context once:

```go
return fmt.Errorf("task cancel: %w", err)
```

Do not wrap the same noun at every layer until the message reads like a haunted
stack trace.

## Interactivity

Interactive behavior is allowed only when stdin is a terminal and the command is
clearly user-driven.

Rules:

- Detect non-interactive use before prompting.
- Provide flags for scripted use instead of requiring prompts.
- Confirmation prompts for destructive actions should show what will be changed.
- `--force` may skip confirmation, but it must not widen the operation.
- Never prompt for secrets if the value can be read from the vault or a standard
  environment variable.

## Destructive Commands

A destructive command deletes, revokes, overwrites, cancels, archives, or sends
something externally visible.

Requirements:

1. The command name must make the action obvious: `delete`, `remove`, `cancel`,
   `revoke`, `send`.
2. Target selection must be explicit. Avoid destructive commands that operate on
   an implicit "current" resource.
3. Bulk destructive operations need either a narrow filter plus confirmation or
   an explicit `--force`.
4. Dry-run support is preferred for broad operations.

Do not make `stella <thing> sync` delete remote data unless the help text and
confirmation say that plainly. Surprise deletion is how tools get uninstalled
with prejudice.

## Configuration and Environment

Use configuration precedence consistently:

```text
flag > environment variable > persisted config > default
```

Document environment variables in help text when they affect behavior. Common
variables include:

| Variable            | Purpose                                                               |
| ------------------- | --------------------------------------------------------------------- |
| `STELLA_SERVER_URL` | Server base URL for CLI-as-client commands                            |
| `STELLA_TOKEN`      | Bearer token for human/operator CLI requests when explicitly provided |
| `LOG_LEVEL`         | CLI logging verbosity                                                 |

Never print secrets. If a command must show that a secret exists, show metadata
or a redacted value.

## Networking and Server Access

For features backed by server state, the CLI should call the generated API
client instead of opening the database, reading server-owned files, or
duplicating business logic.

Pattern:

1. Build a typed request from args and flags.
2. Call `apiclient.Call` / the generated client.
3. Render the response.
4. Return errors with command context.

If the server is unavailable, say how to fix it:

```text
connect to Stella server: connection refused (start it with `stellad server` or set STELLA_SERVER_URL)
```

## Logging and Verbosity

- Normal successful commands should be quiet.
- Progress belongs on stderr only when the operation takes noticeable time.
- Debug logs should be controlled by `LOG_LEVEL` and must not pollute stdout.
- Do not log request bodies containing secrets, tokens, email content, or user
  prompts unless explicitly redacted.

## Compatibility

CLI users script everything. Treat command names, flag names, JSON fields, and
exit behavior as compatibility surfaces.

- Add flags instead of changing existing flag meaning.
- Keep old aliases when renaming commands.
- Avoid changing default output order if users plausibly pipe it.
- Prefer additive JSON fields; do not change field types.
- Remove behavior only after checking docs, tests, and known callers.

**Pre-launch exception.** Stella has not shipped a stable release yet, so there
are no external scripts to protect. Until the first release, prefer full
conformance over compatibility shims: rename commands to the correct shape and
drop legacy aliases (including `rm`-style destructive aliases) outright instead
of keeping them around. After launch, the compatibility rules above apply in
full.

**`vault get <name>` secret exception.** `vault get <name>` deliberately prints
the raw secret value to stdout — it is the explicit single-resource retrieval
used for scripting, and is the one sanctioned exception to "never print
secrets." Every other surface (e.g. `vault list`, `email config get/list`) must
keep secrets masked in both human and JSON output.

## Implementation Checklist

When adding or changing a command under `cmd/stella/`:

1. Does the command follow `stella <noun> <verb>` and existing domain naming?
2. Are primary targets positional and modifiers flags?
3. Are `Usage`, `Description`, and `ArgsUsage` clear in `stella help`?
4. Does stdout contain only command data, with diagnostics on stderr?
5. Does scriptable output support `--json` where appropriate?
6. Are errors actionable and wrapped with useful command context?
7. Are destructive actions explicit and protected from accidental broad impact?
8. Does config precedence follow `flag > env > config > default`?
9. For server-backed state, does the command use the generated API client rather
   than direct database or file writes?
10. Are docs and `internal/agent/prompt/template/system_prompt.tmpl` updated if
    command usage changed?
11. Are command tests updated for args, flags, output, and error behavior?
