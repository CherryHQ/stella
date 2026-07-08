# RSS Batch Processing Workflow

Feed polling and entry bookkeeping run through the native `recally` tool. Saving
articles uses the native tool per [save-workflow.md](save-workflow.md).

## 1. Poll Feeds

Use `recally` with `action=feed_poll` and optional `limit` for the maximum
number of new entries to fetch per feed. Omit `id` to poll all enabled RSS feeds;
non-RSS feeds are skipped server-side. Pass `id` only when you need to poll one
feed. The response contains feed results; each result has a `new_entries` array
of pending entries.

If you need to resume or inspect pending work, use `recally` with
`action=entry_list`, `feed_id`, `status=pending`, and optional `page_size` /
`page_token`.
After marking entries via `entry_update`, re-list from the start instead of paging — the pending set shifts as entries are processed.

## 2. Process Entries in Parallel

Use the `delegate` tool to spawn one delegate per pending entry. Each delegate
independently runs the full save workflow from [save-workflow.md](save-workflow.md)
with `source_type=rss`, then updates the entry.

Use `recally` with `action=entry_update`, `feed_id`, the entry `id`, and `status`:
`saved` with `article_id`, `error` with `error_msg`, or `skipped` for duplicates,
off-topic items, or paywalled content.

Each delegate is self-contained — on failure it marks its own entry as error and
exits without affecting others. Wait for all delegates to finish before counting
results.

Entries with `error` status and fewer than 3 attempts are retried on the next poll
cycle.
