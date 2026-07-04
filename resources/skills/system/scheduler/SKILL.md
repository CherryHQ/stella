---
name: scheduler
description: |
  Manage scheduled jobs. Use when the user wants to create, list, or remove recurring or one-time scheduled tasks. Handles cron schedules, interval-based (every), one-time (at) jobs, and scheduled workflow runs.
metadata:
  author: CherryHQ/stella
  owner_plugin: system/scheduler
  version: "1.0"
---

# Scheduler

Use scheduler for time-based triggers. If the user wants to repeat an accepted goal's plan ("save this goal and run it every morning"), save it as a workflow first, then schedule the workflow. If the scheduled work may be long-running, need human review, or need restart resilience, schedule a workflow or a short prompt that creates an async `task` instead of doing the whole job inline.

**Environment**: The CLI talks HTTP to the running stellad server. `STELLA_TOKEN` is
auto-set; the agent process inherits a reachable `STELLA_SERVER_URL` (default
`http://127.0.0.1:25678`). Scheduler is enabled only when the stellad server is
configured with `scheduler.enabled = true`.

## Add a Job

```bash
stella scheduler add \
    --name "job-name" \
    --message "Prompt or instruction to execute on schedule" \
    --every 1h \
    --session-mode reuse
```

**Schedule types** — use exactly one:

| Flag      | Format                       | Example                        |
| --------- | ---------------------------- | ------------------------------ |
| `--cron`  | Standard cron expression     | `"0 9 * * 1-5"` (weekdays 9am) |
| `--every` | Go duration                  | `30m`, `2h`, `24h`             |
| `--at`    | RFC3339 timestamp (one-time) | `"2024-01-15T14:30:00+08:00"`  |

**Session mode**:

- `reuse` (default): conversation history is preserved across executions
- `new`: starts a fresh session on each execution

**Optional flags**:

- `--agent-id <id>`: run the job on a specific agent (defaults to the default agent)

Output (JSON): job record with `id`, `name`, `message`, `session_mode`, `enabled`, and schedule fields.

## Schedule a Workflow

For the user-facing loop "save this goal and run it daily":

1. Check the workflow CLI syntax: `stella workflow --help` and `stella workflow save --help`.
2. Save the accepted composite goal: `stella workflow save <goal-id> --name <name>`.
3. Read the returned workflow `id`.
4. Check scheduler syntax: `stella scheduler add --help`.
5. Create the timed trigger with `stella scheduler add --workflow <workflow-id> --cron <expr>`.

Do not duplicate historical command syntax from memory; run `--help` before invoking. Scheduled workflows are fully frozen by default. If the workflow is partially frozen, only schedule it when the user explicitly wants live replanning and the CLI help shows the required opt-in flag.

Each scheduled fire instantiates a fresh root goal. The scheduler skips only when the previous run completed instantiation and its root goal is still active; failed instantiation does not block the next tick, and a stalled instantiation is resumed instead of duplicated.

## List Jobs

```bash
stella scheduler list --json
```

Human-readable format omits `--json`. Use `--json` when you need to parse IDs.

## Remove a Job

```bash
stella scheduler remove <job-id>
```

## Check Before Adding

Always list first to avoid duplicates:

```bash
stella scheduler list --json
```

Look for a job with the target name. If found, skip creation and report the existing job to the user.

## Patterns

### Scheduled async task

```bash
stella scheduler add \
    --name "weekly-audit" \
    --message "Create an async task to audit the project and request review before making user-visible changes." \
    --cron "0 9 * * 1" \
    --session-mode new
```

### Recurring task (cron)

```bash
stella scheduler add \
    --name "daily-digest" \
    --message "Run the daily digest and summarize for the user." \
    --cron "0 8 * * *" \
    --session-mode reuse
```

### Recurring task (interval)

```bash
stella scheduler add \
    --name "hourly-check" \
    --message "Check for new items and notify the user if found." \
    --every 1h \
    --session-mode reuse
```

### One-time reminder

```bash
stella scheduler add \
    --name "meeting-reminder" \
    --message "Remind the user about their 3pm meeting." \
    --at "2024-01-15T14:45:00+08:00"
```

## Job Template Subscriptions

Platform-provided templates are opt-in scheduled jobs with platform-managed prompts. You cannot edit the message of a subscription job; the prompt is resolved from the template registry.

**List available templates** (shows subscription status per template):

```bash
stella scheduler templates --help
stella scheduler templates --json
```

**Subscribe to a template**:

```bash
stella scheduler subscribe --help
stella scheduler subscribe recally-rss
stella scheduler subscribe recally-rss --every 12h   # override default schedule
```

One subscription per template is allowed. If already subscribed, a friendly message is printed and the command exits with an error.

**Unsubscribe from a template**:

```bash
stella scheduler unsubscribe --help
stella scheduler unsubscribe recally-rss
```

If not subscribed, prints a message and exits successfully. Internally looks up the subscribed job ID from the templates list, then deletes it.

## Limitations

- Scheduler must be enabled on the server (`scheduler.enabled = true` in config).
- One-time jobs (`--at`) with a past timestamp are rejected.
- Plugin-owned jobs cannot be modified.
- Subscription job prompts are read-only; message edits are rejected by the server.
