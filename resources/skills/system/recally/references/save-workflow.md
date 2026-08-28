# Enriched Article Save Workflow

Load this reference only when the user asks to summarize, organize, evaluate, tag, or rate a single article. A bare “save this URL” request follows the **Capture one URL** workflow in `SKILL.md` instead.

## 1. Capture once

Start with the capture script in `SKILL.md`. It writes the article to a sandbox file and returns only compact fetch metadata. Never print the body, move it through the model, or re-fetch the page to obtain metadata that the capture already returned.

The capture flow already retries with `tap fetch --lp`. If that remains thin or fails, escalate to Jina Reader, then `tap fetch -b`, stopping at the first useful result. A 404 is terminal; a 401/403 after those fallbacks means login or a paywall is required.

## 2. Generate Metadata

Read the captured `content_path` only to understand the article and generate metadata. Do not rewrite, clean, trim, normalize, or remove markers from the fetched file before saving it. Produce: **Title**, **Author**, **Tags** (3-7 lowercase), **Source Type**, **Worth-Reading tier**, and a **structured summary**.

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

`recally_article_save` never fetches the URL itself. That is why steps 1-2 exist. Content is required for a new article; saving an already-saved URL with refreshed content updates the article.

Call `recally_article_save` directly when it is listed. Otherwise use `tools.invoke("recally_article_save", ...)` inside `code`; the exact name and arguments are documented here, so do not search for or describe it first. Pass the fetched body as `content_path` using its sandbox-visible `$TMPDIR` path. Do not embed the Markdown in JavaScript or another tool argument. Each item should also include the generated title, author, structured summary, tags, source type, published time when available, and `worth_reading` metadata.

```js
return await tools.invoke("recally_article_save", {
  articles: [{
    url,
    content_path: "<captured content_path>",
    title,
    summary,
    tags,
    source_type: "web"
  }]
});
```

The capture fields remain the archive baseline. For this enriched workflow, add:

- a model-authored structured summary
- 3-7 lowercase tags
- `worth_reading` metadata set to exactly `Top pick`, `Good read`, or `Skim`
- author and published time when the capture returned them

Do not invent a missing author or publication date. The source type is `web` unless the URL is known to be Twitter/X, YouTube, GitHub, RSS, or a PDF.

**Output**: the save action returns per-item results with `url`, `id`, and `status` (`created`, `updated`, or `error`). Do not echo raw IDs unless the user asks; summarize what was saved.

## 4. Optional Share

Only create a public link when the user asks. `share` is the exact tool name; use `action=article`, the saved article id, and the requested expiry. Do not search for or describe it.

When both tools are behind Code, save and share in one Code call. This is the reason to use Code: the intermediate article id stays between tools instead of returning to the model.

```js
const saved = tools.json(await tools.invoke("recally_article_save", {
  articles: [{
    url,
    content_path,
    title,
    summary,
    tags,
    source_type: "web"
  }]
}));

const article = Array.isArray(saved.results)
  ? saved.results.find(result => result.status !== "error")
  : undefined;
if (!article) return saved;

try {
  const shared = await tools.invoke("share", {
    action: "article",
    article_id: article.id,
    expires_in: "7d"
  });
  return { saved, shared };
} catch (error) {
  return { saved, share_error: error.value || String(error) };
}
```

When `recally_article_save` and `share` are directly listed native tools, call them directly in sequence; native tool results cannot be chained without returning to the model.

To refresh an existing article: run the capture script again on the same URL (it reuses the same hashed filename), then call `recally_article_save` again with the refreshed content.
