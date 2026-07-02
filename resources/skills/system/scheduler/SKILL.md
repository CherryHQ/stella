---
name: scheduler
description: |
  Manage scheduled jobs. Use when the user wants to create, list, update, pause, resume, or remove recurring or one-time scheduled tasks. Handles cron schedules, interval-based (every), one-time (at) jobs, and platform job templates.
metadata:
  author: CherryHQ/stella
  owner_plugin: system/scheduler
  version: "1.0"
---

# Scheduler

Use scheduler for time-based triggers. If the scheduled work may be long-running, need human review, or need restart resilience, schedule a short prompt that creates an async goal instead of doing the whole job inline.

Agents manage schedules with the native `scheduler` tool. The scheduler CLI remains the human/operator surface; do not use it from an agent session when the native tool is available.

## Actions

- `create`: create a cron, interval, or one-time job. Provide `name`, `message`, exactly one schedule field (`cron`, `every`, or `at`), and optional `session_mode` (`reuse` or `new`).
- `list`: list this agent's scheduled jobs.
- `get`: inspect one job by `id`.
- `update`: change editable fields on a job.
- `pause` / `resume`: disable or re-enable a job without deleting it.
- `delete`: remove a job.

## Schedule types

| Field   | Format                       | Example                        |
| ------- | ---------------------------- | ------------------------------ |
| `cron`  | Standard cron expression     | `"0 9 * * 1-5"` (weekdays 9am) |
| `every` | Go duration                  | `30m`, `2h`, `24h`             |
| `at`    | RFC3339 timestamp (one-time) | `"2024-01-15T14:30:00+08:00"`  |

## Check before adding

Always run `action=list` first to avoid duplicates. If a job with the target name already exists, skip creation and report the existing job to the user.

## Patterns

### Scheduled async goal

Create a scheduler job whose `message` asks the agent to create a goal, for example: "Create an async goal to audit the project and request review before making user-visible changes." Use `session_mode: "new"` for independent scheduled work.

### Recurring job

Use `cron` for calendar schedules and `every` for fixed intervals. Keep the scheduled prompt short and specific.

### One-time reminder

Use `at` with an RFC3339 timestamp. Past timestamps are rejected.

## Job template subscriptions

Platform-provided templates are opt-in scheduled jobs with platform-managed prompts. You cannot edit the message of a subscription job; the prompt is resolved from the template registry.

Use `scheduler` tool `action=create` with `template_key` to subscribe, plus optional schedule override fields such as `every`. One subscription per template is allowed. To unsubscribe, delete the subscribed job by id.

Common templates include:

- `recally-rss` — poll RSS feeds.
- `recally-digest` — generate reading digests.

## Limitations

- Scheduler must be enabled on the server.
- One-time jobs (`at`) with a past timestamp are rejected.
- Plugin-owned jobs cannot be modified.
- Subscription job prompts are read-only; message edits are rejected by the server.
