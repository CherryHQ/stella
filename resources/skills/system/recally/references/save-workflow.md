# Article Save Workflow

Use this workflow whenever saving a single article — whether triggered by a user request or as part of RSS batch processing.

## 1. Fetch to File

Always redirect to a temp file — never capture to a variable. `tap fetch` output can be 100KB+.

Use a URL-derived hash for the filename — avoids collisions:

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

## 2. Generate Metadata

Read `$f` and produce: **Title**, **Author**, **Tags** (3-7 lowercase), **Source Type**, **Worth-Reading tier**, and a **structured summary**.

**Worth-Reading tier** — pick exactly one full label:

- `⭐ Top pick` — high novelty or insight density
- `👍 Good read` — solid and informative
- `📖 Skim` — low depth or mostly known

**Structured summary** — write to a separate temp file in Wall Street Journal style (clear, professional, neutral):

```bash
sf=/tmp/recally-summary-$(echo -n "<url>" | md5 | cut -c1-8).md
```

The file must contain exactly these sections:

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

```bash
stella recally save "<url>" --json \
    --content-file "$f" \
    --title "..." --author "..." --summary "$(cat "$sf")" \
    --tags "tag1" --tags "tag2" \
    --source-type web \
    --published-at "2024-01-01T00:00:00Z" \
    --metadata '{"worth_reading":"<tier-text-only>"}'
```

- Positional `<url>` is required.
- `--published-at` is RFC3339 (use `.published` from `--json`; omit if not available).
- `tier-text-only` is `Top pick`, `Good read`, or `Skim` — no emoji.
- `--source-type`: web / twitter / youtube / github / rss / pdf.

**Output** (`--json`): the saved article resource, including `id`, `file_path`, `url`, `title`, `status`, `source_type`, and `saved_at`.

To re-fetch and refresh an existing article: recompute `$f` from the URL hash, re-fetch, then re-run save with `--content-file $f`.
