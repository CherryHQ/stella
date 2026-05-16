---
title: Scheduling
---

## What You Can Schedule

Stella can run tasks on a schedule — reminders, periodic checks, automated reports, anything you can describe in natural language. Jobs persist across restarts, so once you set something up, it keeps running until you remove it.

There are three types of schedules:

- **Recurring (interval)** — runs every N minutes/hours. Example: "Check my email every 30 minutes."
- **Recurring (cron)** — runs on a cron schedule. Example: "Every weekday at 9am, summarize my calendar."
- **One-time** — runs once at a specific time, then removes itself. Example: "Remind me at 3pm to call the dentist."

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

Open the Web UI to see all your scheduled jobs. You can:

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

## Heartbeat Monitoring

Heartbeat is a special scheduling feature that watches a file on your system. You write instructions in a `HEARTBEAT.md` file, and Stella periodically checks it. If the file has content, Stella reads it, decides whether action is needed, and executes the instructions.

This is useful for:

- Integrating with external tools that can write to a file
- Creating a simple "inbox" that Stella monitors
- Triggering Stella from cron jobs or CI pipelines

### Setting Up Heartbeat

Configure heartbeat from the Web UI:

1. Enable heartbeat monitoring.
2. Set the poll interval (for example, every 10 minutes).
3. Set the path to your heartbeat file (for example, `HEARTBEAT.md`).

When Stella detects content in the file, she reads it, acts on it, and sends you the result through your configured notification channel.

## Tips

- **Use cron for precise timing.** Cron expressions give you fine control: `0 9 * * 1-5` means "9am on weekdays." Use interval (`--every`) for simpler "every N minutes" patterns.
- **One-time jobs clean up after themselves.** After a one-time job runs, it is automatically removed from the schedule.
- **Use "reuse" mode for monitoring tasks.** If the job is tracking something over time (like a project's progress), reuse mode lets Stella reference what she saw in previous runs.
- **Combine with other features.** Scheduled jobs can use any of Stella's capabilities — reading assistant, skills, memory. For example, schedule a job to check your RSS feeds and summarize new articles every morning.
