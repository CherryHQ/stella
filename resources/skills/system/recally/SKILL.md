---
name: recally
description: |
  Reading assistant for saving, organizing, and recalling web content. Use when the user says "save this article", "read this link", "summarize this", "check my feeds", "add to my library", or asks about previously saved content. Handles articles, tweets, YouTube videos, GitHub repos, PDFs, and RSS feeds. Articles are stored as markdown files with metadata and indexed for fast search.
metadata:
  author: CherryHQ/stella
  owner_plugin: system/recally
  version: "1.0"
---

# Recally - Reading Assistant

**Environment**: The CLI talks HTTP to the running stellad server. `STELLA_TOKEN`
is auto-set; the agent process inherits a reachable `STELLA_SERVER_URL` (default
`http://127.0.0.1:25678`). Do not pass user identity flags. Do not try to
open the SQLite database directly.

## References

| Topic                                       | File                                                             |
| ------------------------------------------- | ---------------------------------------------------------------- |
| Saving an article (fetch → generate → save) | [references/save-workflow.md](references/save-workflow.md)       |
| RSS batch processing                        | [references/rss-workflow.md](references/rss-workflow.md)         |
| Twitter/X feed discovery                    | [references/twitter-workflow.md](references/twitter-workflow.md) |
| Website (no-RSS) feed discovery             | [references/website-workflow.md](references/website-workflow.md) |

## CLI syntax

The `stella recally` CLI is the source of truth for command syntax and examples. Before running a Recally command, read that command's help output.

## Search and Retrieve

Read these first:

```bash
stella recally search --help
stella recally list --help
stella recally read --help
```

**Workflow**: `search` → `read` for details. Never assume content without reading.

## Manage Articles

Read these first:

```bash
stella recally save --help
stella recally update --help
stella recally delete --help
```

## Feeds

Read these first:

```bash
stella recally feed add --help
stella recally feed poll --help
stella recally feed list --help
stella recally feed remove --help
stella recally feed entry add --help
stella recally feed mark --help
```

`feed add` sniffs the kind from the URL: `x.com` / `twitter.com` hosts become
`twitter`, everything else defaults to `rss`. Pass `--kind rss|twitter|website` to
force it. Use `--kind website` for a page that lists items but has no RSS.

- **rss** feeds: poll server-side, then process pending entries — see [references/rss-workflow.md](references/rss-workflow.md).
- **twitter** feeds: discover entries via the skill — see [references/twitter-workflow.md](references/twitter-workflow.md).
- **website** feeds: scrape item links from a no-RSS page — see [references/website-workflow.md](references/website-workflow.md).

`feed entry add` is the source-agnostic discovery sink: it dedups on `(feed-id, guid)`
and prints `new` or `dup`. YouTube channels work today with no new code — subscribe
to the channel's RSS feed (`https://www.youtube.com/feeds/videos.xml?channel_id=...`).

## Daily Digest

Read this first:

```bash
stella recally digest --help
```

Format for user:

```
Reading Digest for [Date]
📚 Yesterday's saves ([count]): [title] - [summary], ...
📖 Your library: [total] articles ([unread] unread, [read] read, [starred] ⭐)
🔔 Worth revisiting: [count] unread articles 3+ days old
🏷️ Trending tags: tag1 (N), tag2 (N), ...
```

## Limitations

- **Search**: metadata-only (title, summary, tags, author). Full-body search requires `read`.
- **Deduplication**: canonical URL; mobile/desktop variants may duplicate.
