---
title: Recally Overview
---

Recally is Stella's reading system. It helps you keep up with web pages, PDFs, RSS feeds, and the topics you care about.

Use Recally when reading is not a one-off task. It is for ongoing attention: industry news, policies, research papers, competitor updates, engineering posts, product announcements, and anything else you do not want to miss.

## Save web pages and PDFs

Save a page or PDF into Recally so it becomes part of your reading list. Stella can extract content, keep the source, and make it available for later reading and search.

## Get AI summaries

Recally can summarize saved content so you can quickly decide what deserves deeper attention.

A good summary should answer:

- What is this about?
- Why does it matter?
- What changed?
- What should I read next?

## Chat with articles

After saving an article, you can ask questions about it:

- What are the main claims?
- What assumptions does the author make?
- Compare this with another article.
- Turn this into a brief for my team.

This turns reading from passive collection into active understanding.

## Automatic polling and digests require a subscription

> **Upgrading from an earlier version?** Automatic RSS polling and daily digest broadcasts have been removed. After upgrading, Stella stops polling your feeds and generating digests until you subscribe manually. Each user must create their own subscription:
>
> 1. Open any agent → **Tasks** tab → **New Schedule** → **From template**.
> 2. Select **recally-rss** to resume periodic feed polling, or **recally-digest** for daily digests.
>
> Your saved feeds and articles are intact — only the automatic scheduling has changed.

## Subscribe to feeds

Recally can subscribe to feeds and keep a live stream of new entries. Use this for sources you care about repeatedly: company blogs, release notes, journals, newsletters, and policy updates.

Beyond RSS, you can follow:

- **Twitter/X accounts** — subscribe with a profile URL like `https://x.com/<handle>` and Recally treats new tweets as feed entries, saved and summarized like any other source. Only profile timelines are subscribable; lists, search, individual posts, and bookmarks are rejected.
- **YouTube channels** — subscribe to the channel's RSS feed (`https://www.youtube.com/feeds/videos.xml?channel_id=...`) to track new uploads.
- **Websites with no RSS** — subscribe to a page that lists items (a blog index, release notes, a "What's new" page) and choose the website feed kind. Recally scrapes the item links from the page and saves each one like any other source.

Recally detects the source type from the URL, so subscribing is the same one step regardless of where the content lives. For RSS-less pages, choose the website kind to tell Recally to scrape the page instead of looking for a feed.

## Maintain a reading list

Use status, tags, starred items, and search to keep the list usable. A reading system that only stores links is just a graveyard. Recally should help decide what to read, what to skip, and what to revisit.

## Generate digests

Digests collect what matters across your saved articles and feeds. They are useful for morning briefings, weekly research review, industry monitoring, or team updates.
