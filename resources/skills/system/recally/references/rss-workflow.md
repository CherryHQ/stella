# RSS Batch Processing Workflow

Feed polling and entry bookkeeping still run through the `stella` CLI — the
native `recally` tool does not expose poll/mark yet. Saving articles uses the
native tool per [save-workflow.md](save-workflow.md).

## 1. Poll Feeds

Before polling, read the command help:

```bash
stella recally feed poll --help
```

Poll enabled feeds with JSON output. The response contains feed results; each result has a `new_entries` array of pending entries.

## 2. Process Entries in Parallel

Use the `delegate` tool to spawn one delegate per pending entry. Each delegate independently runs the full save workflow from [save-workflow.md](save-workflow.md) with `source_type=rss`, then marks the entry.

Before marking, read the command help:

```bash
stella recally feed mark --help
```

Mark entries as `saved` with the saved article ID, `error` with a reason, or `skipped` for duplicates, off-topic items, or paywalled content.

Each delegate is self-contained — on failure it marks its own entry as error and exits without affecting others. Wait for all delegates to finish before counting results.

Entries with `error` status and fewer than 3 attempts are retried on the next poll cycle.
