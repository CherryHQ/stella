---
title: Recally - Reading Assistant
---

## Overview

Recally is Anna's reading assistant — a system for saving, organizing, and recalling web content. It lets you build a personal library of articles, papers, tweets, videos, and any other URL-based content you encounter across conversations.

Unlike simple bookmarks, Recally stores full article content as markdown files with structured metadata (title, summary, tags, author, source type), indexes everything for fast search, and integrates with Anna's agent system so you can ask questions about your saved content later.

## Key Features

- **Universal URL support**: Web articles, Twitter/X posts, YouTube videos, GitHub repos, PDFs, RSS feeds
- **Agent-powered summarization**: The agent fetches and summarizes content using the best tool for each source type
- **Fast metadata search**: Search by title, summary, tags, and author (FTS5 planned for future)
- **RSS subscription**: Subscribe to feeds and get automatic ingestion with the scheduler
- **Daily digest**: Morning summary of saved articles, unread counts, and worth-revisiting content
- **File-based storage**: Full content lives in markdown files; DB holds only lightweight index

## Architecture

The recally CLI is a thin REST client. The running anna server (`anna serve`)
is the only process that touches the recally database and the markdown library
on disk; CLI, web UI, and SDK consumers all talk HTTP.

```
User sends URL → Agent loads recally skill
    |
    | tap/gh/kreuzberg fetch
    v
Agent summarizes + extracts metadata
    |
    | anna recally save --url ... --title ... --summary ...
    v
CLI builds JSON request → POST /api/recally/articles  (Authorization: Bearer ANNA_TOKEN)
    |
    v
anna server: writes markdown file + DB index row
    |
    v
Library: $ANNA_HOME/library/{userID}/articles/{year}/{month}/{day}-{slug}.md
```

### Prerequisites

Before running any `anna recally …` command:

1. The anna server must be running (`anna serve`) on the same host or reachable
   over HTTP.
2. Set `ANNA_TOKEN` to a token issued by your account (the server requires
   `ANNA_VAULT_KEY` for token-based auth to be available).
3. Optional: set `ANNA_SERVER_URL` to point the CLI at a remote server. Default
   is `http://127.0.0.1:25678`.

The CLI never opens the SQLite database directly — that responsibility belongs
exclusively to the server.

### REST API

The full contract lives in [`api/recally.openapi.yaml`](https://github.com/vaayne/anna/blob/main/api/recally.openapi.yaml).
Resources:

- `GET/POST /api/recally/articles` — list/search/upsert
- `GET/PUT/DELETE /api/recally/articles/{id}` — read/update/delete; pass
  `?include=content` on GET to also receive the markdown body
- `GET/POST /api/recally/feeds`, `GET/PUT/DELETE /api/recally/feeds/{id}`
- `POST /api/recally/feeds/{id}/poll` — server fetches RSS, creates pending
  entries
- `GET /api/recally/feeds/{feedId}/entries`, `PUT .../entries/{id}`
- `GET /api/recally/digest`

To regenerate the server interface and client after editing the spec, run
`mise run generate:api`.

### Storage Layout

Articles are stored in an agent-independent, user-scoped library:

```
$ANNA_HOME/
├── library/
│   └── {userID}/
│       └── articles/
│           ├── 2026/
│           │   ├── 04/
│           │   │   ├── 29-go-concurrency-patterns.md
│           │   │   └── 30-rust-memory-safety.md
│           │   └── 05/
│           │       └── 01-llm-context-window-research.md
│           └── 2025/
│               └── 12/
│                   └── 25-year-in-review.md
```

The database stores only an index row with URL, title, summary, tags, status, and the relative file path.

### Source Types

| Type      | Fetch Strategy                | Tool                     |
| --------- | ----------------------------- | ------------------------ |
| `web`     | Readable markdown extraction  | `tap fetch`              |
| `twitter` | Tweet text + media            | `tap fetch`              |
| `youtube` | Metadata + transcript         | `tap fetch`              |
| `github`  | Repo info, issues, PRs        | `gh` + `tap fetch`       |
| `pdf`     | Text extraction               | `kreuzberg extract`      |
| `rss`     | Feed polling → entries → save | `anna recally feed poll` |

## CLI Reference

### Article Commands

#### Save an article

```bash
anna recally save --url <url> \
  --title "Article Title" \
  --summary "Brief summary" \
  --tags "go,concurrency" \
  --source-type web \
  --author "Author Name"
```

- `--url` (required): Original article URL
- `--canonical-url`: Optional canonical URL override for deduplication
- `--title`: Article title
- `--summary`: Brief summary
- `--tags`: Comma-separated tags (can be used multiple times)
- `--source-type`: `web`, `twitter`, `youtube`, `github`, `rss`, `pdf`
- `--author`: Article author
- `--content-file`: Path to file containing content (stdin otherwise)
- `--metadata`: JSON metadata string
- `--published-at`: Original publication date (RFC3339)

Output: JSON with `id`, `file_path`, and `created` (false if article was updated)

#### List articles

```bash
anna recally list [--status unread] [--starred] [--json]
```

Filters:

- `--status`: `unread`, `read`, `archived`
- `--source-type`: Filter by source type
- `--starred`: Show only starred articles
- `--limit`: Maximum results (default: 50)
- `--json`: Output as JSON

#### Search articles

```bash
anna recally search "concurrency patterns" [--limit 20] [--json]
```

Searches title, summary, tags, and author using LIKE-based matching (FTS5 deferred to future phase).

#### Read an article

```bash
anna recally read <article-id>
```

Outputs the full markdown content to stdout.

#### Update an article

```bash
anna recally update <article-id> --status read --starred
```

Updates metadata. Also rewrites the file frontmatter if the file exists.

- `--status`: `unread`, `read`, `archived`
- `--starred`: true/false
- `--summary`: New summary
- `--tags`: New tags (replaces existing)

#### Delete an article

```bash
anna recally delete <article-id>
```

Removes from DB and deletes the file.

### RSS Feed Commands

#### Add a feed

```bash
anna recally feed add <feed-url>
```

Fetches feed metadata and subscribes. Creates entries for existing items as `pending`.

#### List feeds

```bash
anna recally feed list [--json]
```

Shows subscribed feeds with last check time and check interval.

#### Remove a feed

```bash
anna recally feed remove <feed-id>
```

Unsubscribes and removes all entries.

#### Poll feeds

```bash
anna recally feed poll [<feed-id>] [--limit 20] [--json]
```

Polls feed(s) for new entries. Without `feed-id`, polls all enabled feeds. Returns pending and retryable entries (status `pending` or `error` with attempts < 3).

Output includes `feed_id`, `new_entries` count, and `pending` array of entries to process.

#### Mark an entry

```bash
anna recally feed mark <feed-id> <entry-id> --status saved --article-id <article-id>
anna recally feed mark <feed-id> <entry-id> --status skipped
anna recally feed mark <feed-id> <entry-id> --status error --error "timeout fetching"
```

Updates entry status. Auto-increments `attempts` and sets `processed_at`.

- `--status`: `saved`, `skipped`, `error`
- `--article-id`: Required when status is `saved`
- `--error`: Error message when status is `error`

### Digest Command

```bash
anna recally digest [--json]
```

Outputs a structured JSON summary:

- `yesterday`: Articles saved yesterday
- `counts`: Unread/read/archived/starred counts
- `revisit`: Articles worth revisiting (unread > 3 days old)
- `top_tags`: Top tags from this week

## Authentication

The CLI authenticates with `ANNA_TOKEN` for all operations. Agent sandbox sessions receive this token automatically, and Recally resolves the user from the authenticated token.

For direct CLI usage outside Anna, provide a valid `ANNA_TOKEN` in the environment.

## Skill Usage

The `recally` system skill (`internal/resources/skills/system/recally/SKILL.md`) teaches the agent how to use the CLI commands. Users don't need to memorize the CLI — they just say:

- "Save this article [URL]"
- "Summarize this link for me"
- "What did I read about Go concurrency?"
- "Check my RSS feeds"
- "Give me my daily reading digest"

The skill includes:

- URL classification (detects Twitter, YouTube, GitHub, PDFs)
- Per-source fetch strategy (tap, gh, kreuzberg)
- Summary format template
- RSS workflow (poll → iterate → save → mark)
- Digest formatting instructions

## RSS Scheduling

Recally integrates with Anna's scheduler for automatic RSS polling. The skill instructs the agent to create a scheduled job when the user first subscribes to a feed:

```
Scheduler action: add
Name: recally-rss
Schedule: every 1h
Session mode: reuse
Message: Load recally skill. Run anna recally feed poll to check for new RSS entries, then process each pending entry following the recally skill RSS workflow.
```

For daily digests, the agent can create:

```
Scheduler action: add
Name: recally-digest
Schedule: cron 0 8 * * *
Session mode: reuse
Message: Load recally skill. Run anna recally digest and compose a friendly daily reading summary for the user following the recally skill digest format.
```

## Article Lifecycle

```
Saved → Unread → Read → Archived
   ↓
Starred (orthogonal to status)
```

- **Unread**: Default status when saved
- **Read**: User has read the article
- **Archived**: Finished and filed away
- **Starred**: Flagged for quick access (separate from status)

## Duplicate Handling

Articles are deduplicated by canonical URL. The CLI:

1. Computes a deterministic canonical URL from the original URL (lowercase host, strip tracking params, sort query params, remove fragment)
2. Checks for existing `(user_id, canonical_url)` in DB
3. If found: updates metadata (title, summary, tags) and returns `created: false`
4. If not found: creates new article with `created: true`

The agent can pass `--canonical-url` if it discovered a better canonical during fetching (e.g., from `<link rel="canonical">`).

## Retrieval Hierarchy

The agent accesses saved content through a two-step flow:

1. **Search metadata** (`anna recally search`): Fast LIKE-based search over title, summary, tags, author. Returns lightweight index rows.

2. **Read full content** (`anna recally read`): Once the agent identifies relevant articles, it reads the full markdown file to answer specific questions.

This keeps search fast while preserving full content for deep queries.

## Implementation Details

| Component         | Location                                                   |
| ----------------- | ---------------------------------------------------------- |
| CLI command       | `cmd/anna/recally.go`                                      |
| Store layer       | `internal/recally/store.go`                                |
| File manager      | `internal/recally/files.go`                                |
| URL normalization | `internal/recally/urlnorm.go`                              |
| Types             | `internal/recally/types.go`                                |
| Skill file        | `internal/resources/skills/system/recally/SKILL.md`        |
| DB schema         | `internal/db/schemas/tables/articles.sql`                  |
| DB schema (RSS)   | `internal/db/schemas/tables/rss_feeds.sql`                 |
| DB queries        | `internal/db/queries/articles.sql`                         |
| DB queries (RSS)  | `internal/db/queries/rss_feeds.sql`                        |
| Sandbox auth env  | `internal/agent/sandbox_backend.go` (injects `ANNA_TOKEN`) |

## Future Improvements

- **FTS5 full-text search**: Index article body content for deeper search
- **Semantic search**: Vector embeddings for concept-based retrieval
- **Article content extraction**: Better handling of paywalled content
- **Reading progress**: Track how far user has read in long articles
- **Cross-article linking**: Auto-detect related articles by shared tags/topics
