---
title: Reading Assistant
---

## What It Does

Stella's reading assistant lets you save articles, papers, tweets, videos, and other web content into a personal library. She fetches the content, summarizes it, extracts metadata, and organizes everything so you can search and revisit it later.

Think of it as a read-it-later service that lives inside your AI assistant — you save a link, and Stella makes it searchable and summarized.

## Saving Content

Just share a URL with Stella and ask her to save it:

- **"Save this article: https://example.com/go-concurrency-patterns"**
- **"Summarize and save this link for me: [URL]"**
- **"Bookmark this tweet: [URL]"**

Stella detects the type of content (web article, tweet, YouTube video, GitHub repo, PDF) and uses the best method to fetch and extract it. She generates a title, summary, and tags, then stores everything in your library.

Supported content types:

| Type    | What Stella Extracts                   |
| ------- | -------------------------------------- |
| Web     | Full article text, title, author       |
| Twitter | Tweet text and media                   |
| YouTube | Metadata and transcript                |
| GitHub  | Repository info, issues, pull requests |
| PDF     | Extracted text content                 |
| RSS     | Feed entries (see RSS section below)   |

If you save the same URL twice, Stella updates the existing entry instead of creating a duplicate.

## Searching Your Library

Once you have saved content, you can search it anytime:

- **"What did I read about Go concurrency?"**
- **"Find my saved articles about Rust."**
- **"Show me what I saved last week."**
- **"Do I have anything bookmarked about database migrations?"**

Stella searches across titles, summaries, tags, and authors. When she finds a match, she can read the full content to answer specific questions about it.

You can also browse your library:

- **"List my unread articles."**
- **"Show my starred articles."**
- **"What's in my reading archive?"**

### Article Status

Each saved article has a status:

- **Unread** — newly saved, not yet reviewed
- **Read** — you have read it
- **Archived** — finished and filed away
- **Starred** — flagged for quick access (independent of status)

You can ask Stella to update these: "Mark that Go article as read" or "Star the article about Rust memory safety."

## RSS Feeds

You can subscribe to RSS feeds, and Stella will automatically collect new entries into your library.

### Subscribing to a Feed

- **"Subscribe to this RSS feed: https://example.com/feed.xml"**
- **"Add this blog's RSS feed to my reading list."**

Stella fetches the feed, shows you what is available, and starts tracking it.

### Automatic Polling

After subscribing, Stella can set up a scheduled job to check your feeds periodically. She will poll for new entries, fetch and summarize each one, and add them to your library automatically.

### Managing Feeds

- **"List my RSS feeds."**
- **"Remove the feed for [blog name]."**
- **"Check my feeds for new articles now."**

## Daily Digest

Stella can give you a daily reading summary:

- **"Give me my reading digest."**
- **"What's new in my reading list?"**

The digest includes:

- Articles saved yesterday
- Your unread, read, archived, and starred counts
- Articles worth revisiting (unread for more than 3 days)
- Your most-used tags this week

You can schedule this as an automatic morning briefing — just ask Stella to "give me a reading digest every morning at 8am."

## Agent and Web UI access

In chat, Stella uses the native Recally tool to save articles, manage feeds, and generate digests. Outside chat, manage your reading list from the Web UI or use the HTTP API for automation.
