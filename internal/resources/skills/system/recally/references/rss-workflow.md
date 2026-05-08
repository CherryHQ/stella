# RSS Batch Processing Workflow

## 1. Poll Feeds

```bash
stella recally feed poll --limit 20 --json
```

Returns an array of feed results, each with a `new_entries` array of pending entries.

## 2. Process Entries in Parallel

Use the `agent` tool to spawn one sub-agent per pending entry. Each sub-agent independently runs the full save workflow from [save-workflow.md](save-workflow.md) with `--source-type rss`, then marks the entry:

**On success:**
```bash
stella recally feed mark <feed-id> <entry-id> --status saved --article-id <article-id>
```

**On failure:**
```bash
stella recally feed mark <feed-id> <entry-id> --status error --error "<reason>"
```

**To skip** (duplicate, off-topic, paywalled):
```bash
stella recally feed mark <feed-id> <entry-id> --status skipped
```

Each sub-agent is self-contained — on failure it marks its own entry as error and exits without affecting others. Wait for all sub-agents to finish before counting results.

Entries with `error` status and fewer than 3 attempts are retried on the next poll cycle.
