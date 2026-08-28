# Article Save Workflow

Use this workflow whenever saving a single article, whether triggered by a user request or as part of RSS batch processing.

## 1. Fetch to File

Always redirect to a temp file. Never capture the body in a shell variable. `tap fetch` output can exceed 100 KB.

Use a URL-derived hash to avoid collisions:

```bash
f="$TMPDIR/recally-$(printf '%s' '<url>' | md5 | cut -c1-8).md"
```

If the URL directly serves Markdown or plain text, use `curl -fsSL "<url>" -o "$f"`. For web pages, try each extraction method in order and stop at the first success:

```bash
# Default web extraction
tap fetch "<url>" > "$f"

# JS-heavy page (fast, no Chrome)
tap fetch --lp "<url>" > "$f"

# Jina Reader when tap returns thin content
curl -fsSL "https://r.jina.ai/<url>" > "$f"

# Full browser rendering
tap fetch -b "<url>" > "$f"

# Browser plus network intercept for SPAs that load content through APIs
tap browser open "<url>" && tap browser network wait --url-pattern "*/api/*" --body > "$f"

# Load tap-web skill only for authentication flows, then re-fetch with -b
```

**When you need metadata** (title, author, published-at) without a second fetch:

```bash
m="$TMPDIR/recally-$(printf '%s' '<url>' | md5 | cut -c1-8)-meta.json"
tap fetch --json "<url>" > "$m"
jq -r .markdown "$m" > "$f"
# Then: read .title/.author/.published/.description from "$m" for save fields
```

**Errors**:

- 403/401: escalate through `--lp`, Jina, then `-b`. If all fail, report paywall/login-required.
- 404: dead link, inform user, stop.
- Empty body (<100 chars): try next method; if all empty, save what exists and warn.

## 2. Generate Metadata

Read `$f` only to understand the article and generate metadata. Do not rewrite, clean, trim, normalize, or remove markers from the fetched file before saving it. Produce: **Title**, **Author**, **Tags** (3-7 lowercase), **Source Type**, **Worth-Reading tier**, and a **structured summary**.

**Worth-Reading tier**: pick exactly one value:

- `Top pick`: high novelty or insight density
- `Good read`: solid and informative
- `Skim`: low depth or mostly known

**Structured summary**: generate it in Wall Street Journal style (clear, professional, neutral). It is model-authored text and can be passed directly as `summary`; do not copy the fetched article body into the model merely to move it between tools.

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

`recally` tool `action=save` never fetches the URL itself. That is why steps 1-2 exist. Content is required for a new article; saving an already-saved URL with refreshed content updates the article.

Call `recally` directly when it is listed. Otherwise use `tools.invoke("recally", ...)` inside `code`; the exact name and arguments are documented here, so do not search for or describe it first. Pass the fetched body as `content_path` using its sandbox-visible `$TMPDIR` path. Do not embed the Markdown in JavaScript or another tool argument. Each item should also include the generated title, author, structured summary, tags, source type, published time when available, and `worth_reading` metadata.

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
- `worth_reading` metadata value: `Top pick`, `Good read`, or `Skim`, with no emoji

**Output**: the save action returns per-item results with `url`, `id`, and `status` (`created`, `updated`, or `error`). Do not echo raw IDs unless the user asks; summarize what was saved.

## 4. Optional Share

Only create a public link when the user asks. `share` is the exact tool name; use `action=article`, the saved article id, and the requested expiry. Do not search for or describe it.

When both tools are behind Code, save and share in one Code call. This is the reason to use Code: the intermediate article id stays between tools instead of returning to the model.

```js
const saved = tools.json(await tools.invoke("recally", {
  action: "save",
  articles: [{
    url,
    content_path,
    title,
    summary,
    tags,
    source_type: "web"
  }]
}));

const article = saved.results.find(result => result.status !== "error");
if (!article) return saved;

return await tools.invoke("share", {
  action: "article",
  article_id: article.id,
  expires_in: "7d"
});
```

When `recally` and `share` are directly listed native tools, call them directly in sequence; native tool results cannot be chained without returning to the model.

To re-fetch and refresh an existing article: recompute `$f` from the URL hash, re-fetch, then call `recally` `action=save` again with the refreshed content.
