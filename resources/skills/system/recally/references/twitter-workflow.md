# Twitter Feed Workflow

Discovery workflow for feeds with `kind=twitter`. Go owns dedup and storage; this
workflow only lists tweets and pushes them as entries. Correctness rests on guid
dedup, **not** a watermark — a failed run just discovers nothing, and the next run
re-lists and Go drops the duplicates.

## 1. Identify Twitter feeds

Use `recally` `action=feed_list` to list feeds. Process each feed whose `kind` is `twitter`. The feed's metadata may hold the stable numeric X user id (rename-proof); fall back to the handle in the feed `url` if it is missing.

## 2. List recent tweets

Use the FxEmbed profile-statuses tap script (see the tap-web skill). Prefer the
numeric id; `since` is a best-effort optimization only — never rely on it for
correctness.

```bash
tap site twitter/fxembed-profile-statuses handle=id:<numeric-user-id> -f json
```

For each returned status:

- **Skip retweets** by default (`is_repost` / `is_retweet` true).
- Map fields:
  - `guid` = tweet id (stable; the dedup key)
  - `url` = tweet url
  - `title` = tweet text; when empty, fall back to `(media: <author>)`

## 3. Push entries (Go dedups)

Use `recally` with `action=entry_add`, `feedId`, tweet ID as `guid`, tweet URL as
`url`, and tweet text as `title`.

Add one feed entry per tweet. The result reports whether the entry was inserted or already existed. Pinned and edited tweets
are handled automatically by guid dedup — just push them all. Stop pushing a feed
once you hit a run of duplicate results if you want to save calls, but pushing extras
is harmless.

## 4. Process pending entries

New entries land as `pending`, exactly like RSS entries. Process them with the
standard [save-workflow.md](save-workflow.md) using `source_type=twitter`
(fetch tweet content via `tap site twitter/fxembed-status id=<tweet-id>`), then
mark each entry saved / skipped / error as described in
[rss-workflow.md](rss-workflow.md).
