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

Recally is Anna's built-in reading assistant. Save articles, tweets, videos, and documents to your personal library, organize them with tags, and search them later. RSS feeds can be monitored for automatic ingestion.

**Environment**: The CLI authenticates with `ANNA_TOKEN` from the sandbox environment (automatically set). Do not pass user identity flags; Recally resolves the user from the token.

## Core Workflow: Save an Article

When the user shares a URL to save:

### 1. Classify the URL

| Pattern | Source Type | Fetch Strategy |
|---------|-------------|----------------|
| `twitter.com`, `x.com` | twitter | `tap fetch <url>` for content |
| `youtube.com`, `youtu.be` | youtube | `tap fetch <url>` for metadata |
| `github.com` | github | `gh repo view <owner/repo>` or `tap fetch` |
| `*.pdf` or PDF content-type | pdf | `kreuzberg extract` if local; `tap fetch` + kreuzberg if remote |
| Everything else | web | `tap fetch <url>` for readable markdown |

### 2. Fetch and Summarize

Fetch the content using the appropriate tool, then generate a structured summary:

```bash
# General web article
tap fetch https://example.com/article

# GitHub repo
gh repo view owner/repo --json name,description,primaryLanguage,stargazersCount

# PDF (download then extract)
tap fetch https://example.com/paper.pdf -o /tmp/paper.pdf
kreuzberg extract /tmp/paper.pdf
```

**Summary format** (produce this for saving):
- **Title**: Original title or a concise 5-8 word description
- **Author**: If identifiable, else empty
- **Summary**: 2-4 sentence overview of the key points
- **Tags**: 3-7 relevant keywords (lowercase, no spaces)
- **Source Type**: One of `web`, `twitter`, `youtube`, `github`, `pdf`, `rss`

### 3. Save to Library

```bash
echo '<full-markdown-content>' | anna recally save \
    --url "https://original.url" \
    --canonical-url "https://canonical.url" \
    --title "Article Title" \
    --author "Author Name" \
    --summary "Brief summary..." \
    --tags "tag1" --tags "tag2" --tags "tag3" \
    --source-type web \
    --metadata '{"key":"value"}'
```

**Key flags**:
- `--url` (required): Original URL as provided by user
- `--canonical-url` (optional): If you discovered a better canonical URL during fetching (e.g., from `<link rel="canonical">`), pass it here for deduplication
- `--source-type`: One of `web`, `twitter`, `youtube`, `github`, `rss`, `pdf`
- `--metadata`: JSON object with any extra metadata you want to preserve

The CLI normalizes URLs deterministically (strip tracking params like `utm_*`, `fbclid`, lowercase host, sort query params). If `--canonical-url` is provided, it's used for deduplication instead of the computed canonical.

**Output**: JSON with `id`, `file_path`, and `created` (true if new, false if updated existing).

## Search and Retrieve

### Search Articles (Metadata-based)

**Current implementation uses LIKE search over title, summary, tags, and author.** Full-text search (FTS5) is planned for a future release.

```bash
# Search for articles
anna recally search "machine learning" --limit 20

# List with filters
anna recally list --status unread --starred --limit 10
```

### Read Full Content

```bash
# Read the full markdown file to answer questions
anna recally read <article-id>
```

**Search workflow**: Run `search` first to find candidate articles by metadata, then `read` specific articles to answer detailed questions. Do not assume you know the content without reading.

## Manage Articles

```bash
# Update metadata (status, starred, summary, tags)
anna recally update <id> --status read --starred

# Mark as read
anna recally update <id> --status read

# Delete
anna recally delete <id>
```

## RSS Feeds

### Subscribe to a Feed

```bash
anna recally feed add https://example.com/feed.xml
```

### Poll for New Entries

```bash
# Poll all feeds
anna recally feed poll --limit 20 --json

# Poll specific feed
anna recally feed poll <feed-id> --limit 20 --json
```

**Output**: JSON array with `feed_id`, `feed_title`, `new_entries` count, and `pending` array of entries to process.

### Process RSS Entries

For each entry in the `pending` array:

1. **Fetch the content** using `tap fetch <entry.url>`
2. **Generate summary** following the format above
3. **Save the article** via `anna recally save` with `--source-type rss`
4. **Mark the entry** as saved:

```bash
anna recally feed mark <entry-id> --status saved --article-id <article-id>
```

**Error handling**: If fetching or saving fails, mark with error status:

```bash
anna recally feed mark <entry-id> --status error --error "Failed to fetch: timeout"
```

Entries in `error` status with fewer than 3 attempts are retried on the next poll cycle.

### Skip Entries

If an entry should not be saved (duplicate, off-topic, paywalled):

```bash
anna recally feed mark <entry-id> --status skipped
```

### List and Manage Feeds

```bash
# List subscribed feeds
anna recally feed list

# Remove a feed
anna recally feed remove <feed-id>
```

## Batch Processing (RSS)

When processing multiple RSS entries, use the fast model for summaries to control costs:

> **Instruction for agent**: Use `model_fast` for batch RSS summarization.

Process entries sequentially. If one fails, mark it as error and continue to the next. Do not stop the batch for a single failure.

## Daily Digest

Generate a personalized reading summary for the user:

```bash
anna recally digest
```

**Output**: JSON with these fields:
- `saved_yesterday`: Array of articles saved in the last 24 hours
- `saved_yesterday_count`: Number of articles saved yesterday
- `unread_count`: Total unread articles
- `read_count`: Total read articles
- `archived_count`: Total archived articles
- `starred_count`: Total starred articles
- `worth_revisiting`: Array of unread articles older than 3 days (up to 10)
- `worth_revisiting_count`: Number of revisiting candidates
- `top_tags`: Array of most frequent tags from this week (up to 10)
- `total_articles`: Total articles in library

### Digest Format for Users

When presenting the digest, structure it like this:

**Reading Digest for [Date]**

📚 **Yesterday's saves** ([count] articles):
- [Title 1] - brief summary
- [Title 2] - brief summary
...

📖 **Your library**: [total] articles ([unread] unread, [read] read, [starred] ⭐)

🔔 **Worth revisiting**: [count] articles you saved but haven't read yet (3+ days old)
- [Title 1]
- [Title 2]

🏷️ **Trending tags this week**: tag1 (count), tag2 (count), tag3 (count)

Keep it friendly and conversational. Don't list every single tag if there are many.

## Scheduler Integration

Automate RSS polling and daily digests using the `scheduler` tool.

### Automatic RSS Polling

When a user subscribes to their first RSS feed, create a recurring job to check for new entries:

1. **Check if job exists**: Use `scheduler` tool with `action: list` and look for a job named `recally-rss`
2. **If not found, create it**: Use `scheduler` tool with:
   - `action: add`
   - `name: recally-rss`
   - `every: 1h` (poll every hour)
   - `session_mode: reuse` (keep conversation history)
   - `message: "Load recally skill. Run anna recally feed poll to check for new RSS entries, then process each pending entry following the recally skill RSS workflow."`

**Important**: Only create the scheduler job once - when the user first subscribes to a feed. Use `scheduler list` to check for existing `recally-rss` job before adding.

### Automatic Daily Digest

Offer users a daily digest of their reading activity. If they accept:

1. **Check if job exists**: Look for job named `recally-digest` via `scheduler list`
2. **Create if not found**: Use `scheduler` tool with:
   - `action: add`
   - `name: recally-digest`
   - `cron: 0 8 * * *` (every day at 8am)
   - `session_mode: reuse`
   - `message: "Load recally skill. Run anna recally digest and compose a friendly daily reading summary for the user following the recally skill digest format."`

### Managing Scheduled Jobs

To view or remove scheduled recally jobs:

```bash
# List all jobs (via scheduler tool)
scheduler action=list

# Remove RSS polling job
scheduler action=remove id=<recally-rss-job-id>

# Remove digest job
scheduler action=remove id=<recally-digest-job-id>
```

## File Storage

Articles are stored as markdown files at:
```
$ANNA_HOME/library/{userID}/articles/{year}/{month}/{day}-{slug}.md
```

Each file includes YAML frontmatter with metadata (title, author, tags, source, URL, status) followed by the full content.

## Quick Reference

```bash
# Save an article
echo '<content>' | anna recally save --url <url> --title <title> --summary <summary> --tags tag1 --tags tag2 --source-type web

# Search articles
anna recally search <query> [--limit N] [--json]

# List articles
anna recally list [--status unread|read|archived] [--source-type web|twitter|youtube|github|rss|pdf] [--starred] [--limit N]

# Read full article
anna recally read <article-id>

# Update article
anna recally update <id> [--status <status>] [--starred] [--summary <text>] [--tags tag1,tag2]

# Delete article
anna recally delete <id>

# Daily digest (JSON output)
anna recally digest

# RSS feeds
anna recally feed add <url>
anna recally feed list [--json]
anna recally feed remove <feed-id>
anna recally feed poll [<feed-id>] [--limit N] [--json]
anna recally feed mark <entry-id> --status saved|skipped|error [--article-id <id>] [--error <msg>]
```

## Limitations

- **Search**: Currently metadata-only (title, summary, tags, author) using LIKE patterns. Full-body search requires reading individual articles.
- **URL deduplication**: Based on canonical URL. If a site uses different canonical URLs for mobile/desktop or regional variants, duplicates may occur.
- **RSS processing**: Batch summarization can be slow for large feeds. Use `--limit` to control processing volume.
