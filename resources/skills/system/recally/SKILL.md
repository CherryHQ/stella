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

## Search and Retrieve

```bash
stella recally search "query" --limit 20 --json
stella recally list --status unread --starred --source-type web --limit 20 --json
stella recally read <id>
```

`list` filters: `--status` (unread/read/archived), `--source-type` (web/twitter/youtube/github/rss/pdf), `--starred`, `--limit`.

**Workflow**: `search` → `read` for details. Never assume content without reading.

## Manage Articles

```bash
stella recally update <id> --status read --starred
stella recally update <id> --tags "tag1" --tags "tag2"   # replaces existing tags
stella recally update <id> --summary "..."
stella recally delete <id>
```

## Feeds

```bash
stella recally feed add <feed-url>             # kind sniffed from URL (x.com → twitter, else rss); override with --kind
stella recally feed poll --limit 20 --json     # rss fast-path; non-rss feeds are skipped server-side
stella recally feed list --json                # includes each feed's kind
stella recally feed remove <feed-id>
stella recally feed entry add --feed-id <id> --guid <guid> --url <url> --title <title>   # source-agnostic; prints new/dup
```

`feed add` sniffs the kind from the URL: `x.com` / `twitter.com` hosts become
`twitter`, everything else defaults to `rss`. Pass `--kind rss|twitter` to force it.

- **rss** feeds: poll server-side, then process pending entries — see [references/rss-workflow.md](references/rss-workflow.md).
- **twitter** feeds: discover entries via the skill — see [references/twitter-workflow.md](references/twitter-workflow.md).

`feed entry add` is the source-agnostic discovery sink: it dedups on `(feed-id, guid)`
and prints `new` or `dup`. YouTube channels work today with no new code — subscribe
to the channel's RSS feed (`https://www.youtube.com/feeds/videos.xml?channel_id=...`).

## Daily Digest

```bash
stella recally digest
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
