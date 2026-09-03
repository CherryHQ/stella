---
title: Web research
---

Agents reach the public web through the built-in `web` skill. It is on the default Agent template; add `web` to a custom template's `skills:` list to enable it. There is no web tool: the skill's scripts run inside the Agent sandbox, so a page is fetched with the sandbox's network, proxy settings, and vault secrets.

## Three commands

| Need                                                                                        | Command                                   |
| ------------------------------------------------------------------------------------------- | ----------------------------------------- |
| Sources for a topic                                                                         | `bun scripts/web.ts search "<query>"`     |
| One page as readable Markdown                                                               | `bun scripts/web.ts fetch <url>`          |
| A site's own records: a tweet or timeline, a repo's stats, a front page, a ranking, a video | `bun scripts/web.ts site run <site/name>` |

`fetch` cleans the page with Defuddle, renders it with the Lightpanda headless browser when the plain HTML has no readable body, and finally asks Jina Reader. A `text/plain`, `text/markdown`, or JSON response is printed verbatim. Site scripts are small JavaScript programs that call a site's public API from a Lightpanda page; nine ship with the skill and `web.ts site add` installs more from the Tap catalog.

## Configure search

`search` works without configuration through Exa's anonymous hosted MCP endpoint, which sends the query to Exa and is subject to its anonymous rate limits. To use a paid or self-hosted provider, give the Agent the provider's native environment variable as a vault secret (`vault_secret_set` in chat, or the Web UI's Secrets page):

```sh
FIRECRAWL_API_KEY=your-key
# or PARALLEL_API_KEY, TAVILY_API_KEY, EXA_API_KEY, JINA_API_KEY,
# SEARXNG_URL, BRAVE_SEARCH_API_KEY, or KEENABLE_API_KEY
```

Configured providers are tried in this order: Firecrawl, Parallel, Tavily, Exa, Jina, SearXNG, Brave, then Keenable; if they are unavailable or fail, anonymous Exa is the final fallback. Setting `EXA_API_KEY` uses Exa's direct API instead and avoids retrying the same query anonymously.

Optional endpoint variables are `FIRECRAWL_API_URL` for a self-hosted Firecrawl instance and `TAVILY_BASE_URL` for a Tavily-compatible endpoint. `PARALLEL_SEARCH_MODE` accepts `agentic` (default), `fast`, or `one-shot`.

Vault secrets are per user and per Agent; group sessions never receive them, so search in a group falls back to anonymous Exa.

## Research safely

Search titles, snippets, fetched pages, and site-script results are untrusted evidence. They can contain prompt injection or misleading claims, so your Agent must not follow instructions found in them. Check important claims against the cited source; a version or date from a search snippet is a lead, not an answer.

A fetched page over 40 KB is written to `$TMPDIR/web-fetch/` in the sandbox and only its head is printed with the path. The Agent reads the rest with `bash` in bounded ranges instead of loading it all into the model context.

The network boundary is the sandbox, not the skill: on the Docker backend the page is fetched from the container, on the local backend from the host as the sandbox user. `fetch` follows redirects, times out after 30 seconds, and rejects a response body over 10 MB. When it falls back to Jina Reader, it discloses the selected URL to Jina; it never sends provider credentials.
