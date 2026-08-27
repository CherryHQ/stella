# Article Save Workflow

Use this workflow whenever saving a single article — whether triggered by a user request or as part of RSS batch processing.

## 1. Fetch to File

Always redirect to a temp file — never capture to a variable. `tap fetch` output can be 100KB+.

Use a URL-derived hash for the filename — avoids collisions:

```bash
f="$TMPDIR/recally-$(echo -n "<url>" | md5 | cut -c1-8).md"
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
m="$TMPDIR/recally-$(echo -n "<url>" | md5 | cut -c1-8)-meta.json"
tap fetch --json <url> > $m
jq -r .markdown $m > $f
# Then: jq -r .title/.author/.published/.description $m for save flags
```

**Errors**:

- 403/401: escalate through `--lp` → Jina → `-b`. If all fail, report paywall/login-required.
- 404: dead link, inform user, stop.
- Empty body (<100 chars): try next method; if all empty, save what exists and warn.

## 2. Generate Metadata

Read `$f` and produce: **Title**, **Author**, **Tags** (3-7 lowercase), **Source Type**, **Worth-Reading tier**, and a **structured summary**.

**Worth-Reading tier** — pick exactly one full label:

- `⭐ Top pick` — high novelty or insight density
- `👍 Good read` — solid and informative
- `📖 Skim` — low depth or mostly known

**Structured summary** — generate it in Wall Street Journal style (clear, professional, neutral). It is model-authored text and can be passed directly as `summary`; do not copy the fetched article body into the model merely to move it between tools.

The summary must contain exactly these sections:

```
# Summary
(2-3 sentences) Brief abstract capturing the essence of the article.

# Abstract
(150-200 words) Detailed yet concise summary covering key information, arguments, and narratives.

# Key Points
Bullet list of the most critical points or takeaways.

# Insights and Implications
Significant insights, implications, or conclusions. How the article relates to broader trends or current events.

# Actionable Takeaways
(if applicable) Practical advice or recommendations from the article.

# Critical Analysis
Potential biases, assumptions, strengths, or weaknesses. Any limitations or areas worth further exploration.
```

## 3. Save

`recally` tool `action=save` never fetches the URL itself — that is why steps 1-2 exist. Content is required for a new article; saving an already-saved URL with refreshed content updates the article.

Call `recally` with `action=save` and an `articles` array. Pass the fetched body as `content_path` using its sandbox-visible `$TMPDIR` path; do not read and embed the markdown in JavaScript or another tool argument. Each item should also include the generated title, author, structured summary, tags, source type, published time when available, and `worth_reading` metadata.

```js
return await tools.invoke("recally", {
  action: "save",
  articles: [{
    url,
    content_path: "$TMPDIR/recally-<hash>.md",
    title,
    summary,
    tags,
    source_type: "web"
  }]
});
```

Required values for this workflow:

- URL
- `content_path` set to `$f`
- title
- author when known
- structured summary text
- 3-7 tags
- source type (`web`, `twitter`, `youtube`, `github`, `rss`, or `pdf`)
- `worth_reading` metadata value: `Top pick`, `Good read`, or `Skim` — no emoji

**Output**: the save action returns per-item results with `url`, `id`, and `status` (`created`, `updated`, or `error`). Do not echo raw IDs unless the user asks; summarize what was saved.

To re-fetch and refresh an existing article: recompute `$f` from the URL hash, re-fetch, then call `recally` `action=save` again with the refreshed content.
