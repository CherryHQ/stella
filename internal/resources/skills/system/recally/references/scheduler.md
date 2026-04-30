# Scheduler Integration

Automate RSS polling and daily digests using the `scheduler` tool.

## Automatic RSS Polling

When a user subscribes to their first feed, create a recurring job:

1. Check: `scheduler action=list` — look for a job named `recally-rss`
2. If not found, create:
   - `action: add`, `name: recally-rss`, `every: 1h`, `session_mode: reuse`
   - `message: "Load recally skill. Run anna recally feed poll to check for new RSS entries, then process each pending entry following the recally skill RSS workflow."`

Only create once. Check before adding.

## Automatic Daily Digest

Offer a daily digest when the user first asks. If they accept:

1. Check: `scheduler action=list` — look for `recally-digest`
2. If not found, create:
   - `action: add`, `name: recally-digest`, `cron: 0 8 * * *`, `session_mode: reuse`
   - `message: "Load recally skill. Run anna recally digest and compose a friendly daily reading summary for the user following the recally skill digest format."`

## Managing Jobs

```bash
# List jobs
scheduler action=list

# Remove
scheduler action=remove id=<job-id>
```
