---
name: scheduler
description: |
    Manage scheduled tasks. Use when the user asks to schedule something recurring (daily, hourly, weekly), set a one-time reminder, automate a periodic task, or list/cancel existing scheduled jobs. Handles cron-based schedules, interval-based repetition, and one-time execution at a specific time.
metadata:
    author: vaayne/anna
    owner_plugin: system/scheduler
    version: "1.0"
---

# Scheduler - Task Scheduling

**Environment**: Authenticates via `ANNA_TOKEN` (auto-set). Do not pass user identity flags.

## Add a Job

Exactly one of `--cron`, `--every`, or `--at` is required.

### Recurring — cron expression

```bash
anna scheduler add \
    --name "job-name" \
    --message "prompt to execute on schedule" \
    --cron "0 9 * * 1-5" \
    --session-mode reuse
```

### Recurring — interval

```bash
anna scheduler add \
    --name "job-name" \
    --message "prompt to execute on schedule" \
    --every 1h \
    --session-mode reuse
```

`--every` accepts Go durations: `30m`, `2h`, `24h`, etc.

### One-time

```bash
anna scheduler add \
    --name "job-name" \
    --message "prompt to execute once" \
    --at "2024-01-15T14:30:00+08:00"
```

`--at` must be RFC3339. Use the user's local timezone offset.

### Flags

| Flag | Required | Default | Description |
|------|----------|---------|-------------|
| `--name` | yes | — | Short identifier for the job |
| `--message` | yes | — | Prompt sent to the assistant when the job fires |
| `--cron` | one of | — | Cron expression (5-field, no seconds) |
| `--every` | one of | — | Go duration, e.g. `30m`, `1h`, `24h` |
| `--at` | one of | — | RFC3339 timestamp for one-time execution |
| `--session-mode` | no | `reuse` | `reuse` keeps conversation history; `new` starts fresh each run |

Output is JSON with `id`, `name`, `schedule`, `session_mode`, `enabled`, `created_at`.

## List Jobs

```bash
anna scheduler list
```

Returns a JSON array of all scheduled jobs owned by the current user. Plugin-managed jobs are hidden.

## Remove a Job

```bash
anna scheduler remove <job-id>
```

`<job-id>` is the `id` field from `add` or `list` output. Always `list` first to confirm the correct job ID before removing.

## Workflow Guidelines

- **Check before adding**: always `list` first to avoid duplicate jobs with the same name.
- **session_mode**: use `reuse` for stateful tasks (digests, reports), `new` for independent snapshots.
- **cron timezone**: cron runs in the server's local timezone. Ask the user for their timezone if they give a clock time without one.
- **One-time jobs**: `--at` timestamps in the past are rejected; always use a future time.
- **Message content**: write the message as a complete instruction the assistant can act on without context, e.g. `"Run anna recally digest and send a reading summary."` not just `"digest"`.
