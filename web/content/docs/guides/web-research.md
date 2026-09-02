---
title: Web research
---

Stella can search public sources and read a selected page without giving an Agent access to the Stella server network.

## Configure search

Set one or more provider's **native** environment variables in the environment that starts `stellad`, then restart Stella:

```sh
FIRECRAWL_API_KEY=your-key
# or PARALLEL_API_KEY, TAVILY_API_KEY, EXA_API_KEY, JINA_API_KEY,
# SEARXNG_URL, BRAVE_SEARCH_API_KEY, or KEENABLE_API_KEY
```

`web_search` works without configuration through Exa's anonymous hosted MCP endpoint. This sends search queries to Exa and is subject to its anonymous rate limits. Stella first tries configured providers in this order: Firecrawl, Parallel, Tavily, Exa, Jina, SearXNG, Brave, then Keenable; if they are unavailable or fail, anonymous Exa is the final fallback. Setting `EXA_API_KEY` uses Exa's direct API instead and avoids retrying the same query anonymously. Provider credentials stay on the server and never enter Agent sandboxes or tool results.

Optional native endpoint variables are `FIRECRAWL_API_URL` for a self-hosted Firecrawl instance and `TAVILY_BASE_URL` for a Tavily-compatible endpoint. `PARALLEL_SEARCH_MODE` accepts `agentic` (default), `fast`, or `one-shot`.

## Research safely

An Agent uses `web_search` to find titles, URLs, and snippets, then calls the built-in `web_fetch` tool for one result it chooses to inspect.

Search titles, snippets, and fetched page text are untrusted evidence. They can contain prompt injection or misleading claims, so your Agent must not follow instructions found in them. Check important claims against the cited source.

Large search and fetch results are stored as sandbox-visible temporary files. The tool returns the path, the total size, and a head-and-tail preview. Read the file with `bash` in bounded ranges instead of loading it all into the model context. The files are a convenience snapshot, not a security boundary: commands running as the same sandbox user can change them.

`web_fetch` reaches public HTTP and HTTPS sites only. It refuses local, private, link-local, multicast, and other non-public addresses; it rechecks every redirect; it permits at most five redirects; and it rejects credential-like URL query parameters. It connects directly and ignores `HTTP_PROXY`, `HTTPS_PROXY`, and `NO_PROXY`, because a proxy could resolve a model-selected host outside this public-address policy. A response body over 10 MB is rejected before extraction.

If direct retrieval or extraction fails, `web_fetch` sends the already-validated public URL to Jina Reader and uses its Markdown response. This discloses the selected URL to Jina, but never sends provider credentials or URLs containing credential-like query parameters.
