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

Recally is Anna's built-in reading assistant. Save articles, tweets, videos, and documents to your personal library, organize them with tags, and search them later.

**Environment**: Authenticates via `ANNA_TOKEN` (auto-set). Do not pass user identity flags.

## References

| Topic | File | When to read |
|-------|------|--------------|
| RSS processing | [references/rss-workflow.md](references/rss-workflow.md) | Processing pending entries, batch handling, error recovery |
| Scheduler | [references/scheduler.md](references/scheduler.md) | Auto-polling setup, daily digest scheduling |

## Save an Article

### 1. Classify the URL

**First, check for a site script** — structured scripts beat raw fetch for quality and reliability:

```bash
tap site search twitter   # check if a script exists for the domain
tap site <site/action> [key=value]   # run it if found
```

| Pattern | Source Type | Fetch Strategy |
|---------|-------------|----------------|
| `twitter.com`, `x.com` | twitter | `tap site twitter/thread tweet_id=<id>` (thread) or `tap site twitter/user screen_name=<name>`, else `tap fetch --lp <url>` |
| `youtube.com`, `youtu.be` | youtube | `tap site youtube/video id=<video_id>` if script exists, else `tap fetch --lp <url>` |
| `github.com` | github | `gh repo view <owner/repo> --json name,description,primaryLanguage,stargazersCount` or `tap fetch` |
| `*.pdf` / PDF content-type | pdf | `kreuzberg extract` if local; `curl -sL ... -o /tmp/recally-article.pdf && kreuzberg extract` if remote |
| Everything else | web | `tap fetch --json <url>` |

### 2. Fetch to File

Always fetch content to `/tmp/recally-article.md` first. Read that file for summarization, then pass it to `save`. Never pipe content directly.

```bash
# Web article — fetch clean markdown for the saved body
tap fetch https://example.com/article > /tmp/recally-article.md

# JS-heavy page, no login needed — Lightpanda is faster than full Chrome
tap fetch --lp https://example.com/spa > /tmp/recally-article.md

# GitHub repo
gh repo view owner/repo --json name,description,primaryLanguage,stargazersCount > /tmp/recally-article.md

# PDF
curl -sL https://example.com/paper.pdf -o /tmp/recally-article.pdf
kreuzberg extract /tmp/recally-article.pdf > /tmp/recally-article.md
```

**`tap` quick reference** (covers normal recally use — load the tap skill only for auth/CDP flows):

| Command / Flag | Effect |
|----------------|--------|
| `tap site search <domain>` | Check if a structured site script exists |
| `tap site <site/action> [k=v]` | Run a site script (best quality for known sites) |
| `tap fetch <url>` | Clean readable markdown |
| `tap fetch --json <url>` | JSON with `.markdown` (clean text), `.title`, `.author`, `.published`, `.description`, `.domain`, `.wordCount` |
| `--lp` / `--lightpanda` | Fast JS rendering without Chrome — use for SPAs and JS-heavy pages that don't need login |
| `-b` / `--browser` | Full Chrome rendering — use when `--lp` fails or page requires a logged-in session |
| `--wait <dur>` | Fixed post-navigation delay (e.g. `--wait 3s`) |

**Escalation order**: site script → `tap fetch` → `tap fetch --lp` → `tap fetch -b` → load tap skill for auth. Use `tap fetch --json <url> | jq -r .markdown > /tmp/recally-article.md` only when you also need JSON metadata fields.

After fetching, read `/tmp/recally-article.md` and produce a summary: **Title**, **Author**, **Summary** (2-4 sentences), **Tags** (3-7 lowercase keywords), **Source Type**.

### 3. Save

```bash
anna recally save \
    --content-file /tmp/recally-article.md \
    --url "https://original.url" \
    --canonical-url "https://canonical.url" \
    --title "Article Title" \
    --author "Author Name" \
    --summary "Brief summary..." \
    --tags "tag1" --tags "tag2" --tags "tag3" \
    --source-type web \
    --published-at "2024-01-01T00:00:00Z" \
    --metadata '{"key":"value"}'
```

Key flags: `--url` (required), `--content-file`, `--canonical-url` (optional override for deduplication; derive from `.domain` + path if needed), `--source-type`, `--published-at` (RFC3339; use `.published` from `tap fetch --json`), `--metadata`.

URL normalization strips `utm_*`, `fbclid`, lowercases host. `--canonical-url` overrides computed canonical for deduplication.

**Output**: JSON with `id`, `file_path`, `created` (true=new, false=updated).

## Search and Retrieve

```bash
# Search metadata (title, summary, tags, author)
anna recally search "machine learning" --limit 20 --json

# List with filters
anna recally list --status unread --starred --limit 10 --json

# Read full content
anna recally read <article-id>
```

**Workflow**: `search` to find candidates → `read` specific articles for detailed questions. Do not assume content without reading.

## Manage Articles

```bash
anna recally update <id> --status read --starred
anna recally update <id> --tags "tag1" --tags "tag2"
anna recally delete <id>
```

## RSS Feeds

```bash
# Subscribe
anna recally feed add https://example.com/feed.xml

# Poll for new entries (returns JSON with pending entries to process)
anna recally feed poll --limit 20 --json
anna recally feed poll <feed-id> --limit 20 --json

# List / remove
anna recally feed list
anna recally feed remove <feed-id>
```

See [references/rss-workflow.md](references/rss-workflow.md) for processing pending entries.

## Daily Digest

```bash
anna recally digest
```

Output fields: `saved_yesterday`, `saved_yesterday_count`, `unread_count`, `read_count`, `archived_count`, `starred_count`, `worth_revisiting`, `worth_revisiting_count`, `top_tags`, `total_articles`.

**Format for user**:

```
Reading Digest for [Date]

📚 Yesterday's saves ([count]): [title] - [summary], ...
📖 Your library: [total] articles ([unread] unread, [read] read, [starred] ⭐)
🔔 Worth revisiting: [count] unread articles 3+ days old
🏷️ Trending tags: tag1 (N), tag2 (N), ...
```

Keep it friendly and concise.

## Limitations

- **Search**: Metadata-only (title, summary, tags, author). Full-body search requires `read`.
- **Deduplication**: Based on canonical URL; mobile/desktop variants may duplicate.
