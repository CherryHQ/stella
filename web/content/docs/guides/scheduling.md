---
title: Scheduling
---

## Job Templates

Some scheduled tasks are platform-provided templates — pre-configured prompts that run on your behalf. Instead of running automatically for everyone, they are opt-in: you subscribe to the template once and it becomes your own scheduled job. You can change the schedule, pause, or delete it like any other job.

Available templates:

- **recally-rss** — polls your Recally feeds and saves new entries, every 6 hours by default.
- **recally-digest** — generates a reading digest from your saved articles and feeds, every 24 hours by default.

### Subscribe via the Web UI

1. Open any agent and go to the **Goals** tab.
2. Click **New Schedule**.
3. Choose **From template** at the top of the sheet.
4. Select a template card. Templates you have already subscribed to are shown as disabled.
5. Optionally change the schedule (interval or cron). The agent selection and session mode are also adjustable.
6. Click **Create**.

The subscription appears in your scheduled jobs list with a template badge (for example, `Subscription · recally-rss`). The prompt is platform-managed and read-only — if you want a fully custom prompt, create a regular scheduled job instead.

### Subscribe via the CLI

```bash
# List available templates and check subscription status
stella scheduler templates

# Subscribe (uses the template's default schedule)
stella scheduler subscribe recally-rss

# Subscribe with a custom schedule
stella scheduler subscribe recally-rss --every 12h

# Unsubscribe
stella scheduler unsubscribe recally-rss
```

Run `stella scheduler subscribe --help` or `stella scheduler unsubscribe --help` for full flag details.

### Subscribe via the API

```
POST /api/agents/{agentId}/scheduler/jobs
{
  "template_key": "recally-rss",
  "every": "12h"
}
```

Omit `every` (and `cron`) to use the template's default schedule. You cannot pass `message` when subscribing via `template_key`; the prompt is platform-managed.

One subscription per user per template is allowed. A duplicate subscription returns `409 Conflict`.

## What You Can Schedule

Stella can run tasks on a schedule — reminders, periodic checks, automated reports, anything you can describe in natural language. Jobs persist across restarts, so once you set something up, it keeps running until you remove it.

There are three types of schedules:

- **Recurring (interval)** — runs every N minutes/hours. Example: "Check my email every 30 minutes."
- **Recurring (cron)** — runs on a cron schedule. Example: "Every weekday at 9am, summarize my calendar."
- **One-time** — runs once at a specific time, then disables itself. Example: "Remind me at 3pm to call the dentist."

## Natural Language Examples

Just tell Stella what you want. She will create the right kind of job:

- **"Remind me every morning at 9am to review my task list."**
- **"Check my RSS feeds every hour."**
- **"In 30 minutes, remind me to push my branch."**
- **"Every Friday at 5pm, summarize what I accomplished this week."**
- **"At 2024-12-25T10:00:00+08:00, wish me Merry Christmas."**

Stella translates these into the appropriate schedule type and creates the job for you.

## Session Modes

Each scheduled job has a session mode that controls whether Stella remembers past runs:

- **Reuse** (default) — Stella keeps a persistent session for the job. Each run builds on the previous one. Good for ongoing tasks where context matters, like monitoring a project or tracking daily progress.
- **New** — Stella starts fresh each time with no memory of previous runs. Good for independent tasks like weather checks or reminders.

You can specify the mode when creating a job:

- **"Check my email every 30 minutes, start fresh each time."** — uses "new" mode.
- **"Summarize my daily progress every evening, keep the conversation going."** — uses "reuse" mode.

## Managing Jobs

### From the Web UI

Open an agent and choose the **Goals** tab; schedules appear there alongside goals. You can:

- View job status, schedule, and last run time
- Enable or disable jobs
- Edit job settings
- Delete jobs

### From the CLI

```bash
# List all scheduled jobs
stella scheduler list

# List jobs as JSON (useful for scripting)
stella scheduler list --json

# Add a recurring job (interval)
stella scheduler add --name "email-check" \
  --message "Check my email and summarize new messages" \
  --every 30m

# Add a recurring job (cron)
stella scheduler add --name "morning-briefing" \
  --message "Give me a morning briefing" \
  --cron "0 9 * * 1-5"

# Add a one-time reminder
stella scheduler add --name "dentist-reminder" \
  --message "Remind me to call the dentist" \
  --at "2024-12-15T15:00:00+08:00"

# Add a job with a specific session mode
stella scheduler add --name "weather" \
  --message "Check today's weather in Beijing" \
  --every 24h \
  --session-mode new

# Remove a job
stella scheduler remove <job-id>
```

## Tips

- **Use cron for precise timing.** Cron expressions give you fine control: `0 9 * * 1-5` means "9am on weekdays." Use interval (`--every`) for simpler "every N minutes" patterns.
- **One-time jobs disable themselves after firing.** The entry stays in the schedule list (disabled) so its run history and "run now" keep working; delete it if you no longer need it.
- **Use "reuse" mode for monitoring tasks.** If the job is tracking something over time (like a project's progress), reuse mode lets Stella reference what she saw in previous runs.
- **Combine with other features.** Scheduled jobs can use any of Stella's capabilities — reading assistant, skills, memory. For example, schedule a job to check your RSS feeds and summarize new articles every morning.
