---
name: recally
description: |
    Reading assistant for saving, organizing, and recalling web content. Use when the user says "save this article", "read this link", "summarize this", "check my feeds", "add to my library", or asks about previously saved content. Handles articles, tweets, YouTube videos, GitHub repos, PDFs, and RSS feeds. Articles are stored as markdown files with metadata and indexed for fast search.
metadata:
    author: vaayne/anna
    owner_plugin: system/recally
    version: "1.0"
---

# Recally - Reading Assistant

**Environment**: The CLI talks HTTP to the running anna server. `ANNA_TOKEN`
is auto-set; the agent process inherits a reachable `ANNA_SERVER_URL` (default
`http://127.0.0.1:25678`). Do not pass user identity flags. Do not try to
open the SQLite database directly.

## References

| Topic | File |
|-------|------|
| RSS processing | [references/rss-workflow.md](references/rss-workflow.md) |
| Scheduler | [references/scheduler.md](references/scheduler.md) |

## Save an Article

### 1. Fetch to File

Always redirect to a temp file — never capture to a variable, print to screen, or echo content. `tap fetch` output can be 100KB+.

Use a URL-derived hash for the filename every time — avoids collisions in batch and single-article flows:

```bash
f=/tmp/recally-$(echo -n "<url>" | md5 | cut -c1-8).md
```

**Escalation order** — try each in sequence, stop at first success:

```bash
# 1. Default
tap fetch <url> > $f

# 2. JS-heavy page (fast, no Chrome)
tap fetch --lp <url> > $f

# 3. Jina Reader — lightweight alternative when tap returns thin content
curl -s "https://r.jina.ai/<url>" > $f

# 4. Full browser rendering
tap fetch -b <url> > $f

# 5. Browser + network intercept — for SPAs that load content via API calls
tap browser open <url> && tap browser network wait --url-pattern "*/api/*" --body > $f

# 6. Load tap-web skill for auth flows (attach to Chrome, then re-fetch with -b)
```

**When you need metadata** (title, author, published-at) without a second fetch:
```bash
m=/tmp/recally-$(echo -n "<url>" | md5 | cut -c1-8)-meta.json
tap fetch --json <url> > $m
jq -r .markdown $m > $f
# Then: jq -r .title/.author/.published/.description $m for save flags
```

**Errors**:
- 403/401: escalate through `--lp` → Jina → `-b`. If all fail, report paywall/login-required.
- 404: dead link, inform user, stop.
- Empty body (<100 chars): try next method; if all empty, save what exists and warn.

### 2. Summarize

Read `$f` and produce: **Title**, **Author**, **Summary** (2-4 sentences), **Tags** (3-7 lowercase), **Source Type**.

### 3. Save

```bash
anna recally save \
    --content-file $f \
    --url "<url>" \
    --title "..." --author "..." --summary "..." \
    --tags "tag1" --tags "tag2" \
    --source-type web \
    --published-at "2024-01-01T00:00:00Z"
```

`--url` is required. `--published-at` is RFC3339 (use `.published` from `--json`; omit if empty).

**Output** (always JSON): `id`, `file_path`, `created` (true=new, false=updated existing).  
When `created` is false: the record was updated — tags/metadata are replaced, content updated if `--content-file` was provided. Inform the user their library was updated, not an error.

To re-fetch and refresh: compute `$f` from the URL hash, re-fetch, then `anna recally save --content-file $f --url "<url>"`.

## Search and Retrieve

```bash
anna recally search "query" --limit 20 --json
anna recally list --status unread --starred --source-type web --limit 20 --json
anna recally read <id>
```

`list` filters: `--status` (unread/read/archived), `--source-type` (web/twitter/youtube/github/rss/pdf), `--starred`, `--limit`.

**Workflow**: `search` → `read` for details. Never assume content without reading.

## Manage Articles

```bash
anna recally update <id> --status read --starred
anna recally update <id> --tags "tag1" --tags "tag2"   # replaces existing tags
anna recally update <id> --summary "..."
anna recally delete <id>
```

## RSS Feeds

```bash
anna recally feed add <feed-url>
anna recally feed poll --limit 20 --json     # returns pending entries
anna recally feed list
anna recally feed remove <feed-id>
```

See [references/rss-workflow.md](references/rss-workflow.md) for processing pending entries. Use the URL hash pattern for temp files in batch (same as single-article flow).

## Daily Digest

```bash
anna recally digest
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
