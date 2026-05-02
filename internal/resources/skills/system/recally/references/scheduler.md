# Scheduler Integration

Automate RSS polling and daily digests using `anna scheduler` commands.

## Automatic RSS Polling

When a user subscribes to their first feed, create a recurring job:

1. Check: `anna scheduler list` — look for a job named `recally-rss`
2. If not found, create:

```bash
anna scheduler add \
    --name recally-rss \
    --every 1h \
    --session-mode reuse \
    --message "Load recally skill. Run anna recally feed poll to check for new RSS entries, then process each pending entry following the recally skill RSS workflow."
```

Only create once. Check before adding.

## Automatic Daily Digest

Offer a daily digest when the user first asks. If they accept:

1. Check: `anna scheduler list` — look for a job named `recally-digest`
2. If not found, create:

```bash
anna scheduler add \
    --name recally-digest \
    --cron "0 8 * * *" \
    --session-mode reuse \
    --message "Load recally skill. Run anna recally digest and compose a friendly daily reading summary for the user following the recally skill digest format."
```

## Managing Jobs

```bash
# List jobs
anna scheduler list

# Remove
anna scheduler remove <job-id>
```
