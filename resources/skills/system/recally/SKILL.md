---
name: recally
description: |
  Reading assistant for saving, organizing, and recalling web content. Use when the user says "save this article", "read this link", "summarize this", "check my feeds", "add to my library", or asks about previously saved content. Handles articles, tweets, YouTube videos, GitHub repos, PDFs, and RSS feeds. Articles are stored with their metadata and indexed for fast search.
metadata:
  author: CherryHQ/stella
  owner_plugin: system/recally
  version: "1.0"
---

# Recally - Reading Assistant

Use the `recally` tool for the user's reading library. Tool names in this skill are exact: call a listed tool directly when available, otherwise invoke that name through `code`. Do not search for or describe a tool already named here. Do not pass user identity flags or open the database directly. The library is shared across the user's agents.

## References

| Topic                                       | File                                                             |
| ------------------------------------------- | ---------------------------------------------------------------- |
| Saving an article (fetch → generate → save) | [references/save-workflow.md](references/save-workflow.md)       |
| RSS batch processing                        | [references/rss-workflow.md](references/rss-workflow.md)         |
| Twitter/X feed discovery                    | [references/twitter-workflow.md](references/twitter-workflow.md) |
| Website (no-RSS) feed discovery             | [references/website-workflow.md](references/website-workflow.md) |

## Search and retrieve

- Use `recally` with `action=list_articles` to browse saved articles. Keep page sizes small.
- Use `action=get_article` to read one saved article by id. Never assume details without reading.
- Full article bodies are capped by the tool for token safety; tell the user to use the Web UI for the full body when truncated.

## Save articles

`action=save` never fetches the URL itself. Fetch content first (for example with `tap fetch` or browser tools), redirect the body to a sandbox file, generate metadata/summary, then call `recally` with `action=save` and an `articles` batch. Each item should include `url`, `title`, `content_path` (preferred for fetched bodies) or inline `content`, summary/metadata/tags when available, and `source_type`. Never read a large body back into the model merely to pass it to Recally.

When the user also asks for a public link, `share` is the exact tool name and `action=article` accepts the saved article id. Do not search for or describe `share`. In Code Mode, chain `recally` save and `share` in the same Code call so the article id does not return to the model between tools. See the save workflow for the exact pattern.

The save action is batch-safe: partial failures return per-item errors instead of aborting the whole batch.

## Feeds

Use `action=feed_add` to add RSS, Twitter/X, or website feeds. Use `action=feed_list` to inspect feeds and `action=feed_remove` to remove one.

**RSS polling subscription**: RSS feeds are only polled when the user has subscribed to the `recally-rss` scheduler template. After adding a feed, ask whether they want automatic polling; if yes, use `scheduler` with `action=create` and `template_key=recally-rss`. Add schedule override fields such as `every` only when the user asks. Do not subscribe automatically.

- **rss** feeds: poll server-side, then process pending entries. See [references/rss-workflow.md](references/rss-workflow.md).
- **twitter** feeds: discover entries via the skill. See [references/twitter-workflow.md](references/twitter-workflow.md).
- **website** feeds: scrape item links from a no-RSS page. See [references/website-workflow.md](references/website-workflow.md).

YouTube channels work as RSS feeds with `https://www.youtube.com/feeds/videos.xml?channel_id=...`.

## Daily digest

Use `recally` with `action=digest` to read the current digest. For automatic daily digests, ask the user first, then create a scheduler subscription with `action=create` and `template_key=recally-digest`.

Format digest summaries for the user:

```text
Reading Digest for [Date]
📚 Yesterday's saves ([count]): [title] - [summary], ...
📖 Your library: [total] articles ([unread] unread, [read] read, [starred] ⭐)
🔔 Worth revisiting: [count] unread articles 3+ days old
🏷️ Trending tags: tag1 (N), tag2 (N), ...
```

## Limitations

- **Search**: metadata-only (title, summary, tags, author). Full-body search requires `get_article`.
- **Deduplication**: canonical URL; mobile/desktop variants may duplicate.
