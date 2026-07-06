# Website Feed Workflow

Discovery workflow for feeds with `kind=website` — pages that list items but have
**no RSS** (blog indexes, release notes, "What's new", news sections). Go owns
dedup and storage; this workflow only finds item links and pushes them as entries.
Correctness rests on guid dedup, **not** a watermark — a failed run just discovers
nothing, and the next run re-lists and Go drops the duplicates.

## 1. Identify website feeds

Use `recally` `action=feed_list` to list feeds. Process each feed whose `kind` is `website`. The `url` is the index page to scan.

## 2. Fetch the index page

```bash
tap fetch --json <feed-url>
```

Returns `{markdown, title, ...}`. If the page is JS-heavy and the markdown lacks the
item list, escalate per the tap-web skill: `tap fetch --lp <url>`, then
`tap fetch -b <url>`, then `tap browser open <url>` + `tap browser snapshot`.

## 3. Pick item links (skill judgment)

From the fetched page, select the links that are **actual content items** — articles,
posts, releases, videos. This is fuzzy; use judgment:

- **Keep**: links into the site's content (e.g. `/blog/<slug>`, `/posts/...`, dated paths).
- **Skip**: nav, header/footer, pagination ("next", page numbers), category/tag/author
  index pages, "subscribe"/"login"/social links, and ads.
- Resolve relative links to absolute URLs against the feed URL.

If unsure whether the page is an index at all (e.g. it's a single article), push
nothing — `website` is for pages that list items.

## 4. Normalize the guid (dedup key)

A web item has no stable native id, so **guid = the item's canonical URL**:

- Strip tracking query params (`utm_*`, `fbclid`, `gclid`, `ref`, `source`).
- Strip the URL fragment (`#...`).
- Keep meaningful path/query that identifies the item.

Use the same normalized URL for both `--guid` and `--url`.

## 5. Push entries (Go dedups)

Use `recally` with `action=entry_add`, `feedId`, normalized item URL as both
`guid` and `url`, and the link text or heading as `title`.

Add one feed entry per item. The result reports whether the entry was inserted or already existed. Pushing extras is harmless;
stop once you hit a run of duplicate results if you want to save calls. `title` = the link text
or the item's heading.

## 6. Process pending entries

New entries land as `pending`, exactly like RSS entries. Process them with the
standard [save-workflow.md](save-workflow.md) using `source_type=web` (fetch each
item URL), then mark each entry saved / skipped / error as described in
[rss-workflow.md](rss-workflow.md).
