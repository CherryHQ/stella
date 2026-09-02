---
title: Web research
---

Stella can search public sources and read a selected page without giving an Agent access to the Stella server network.

## Enable search

Set one or more provider's **native** environment variables in the environment that starts `stellad`, then restart Stella:

```sh
FIRECRAWL_API_KEY=your-key
# or PARALLEL_API_KEY, TAVILY_API_KEY, EXA_API_KEY,
# SEARXNG_URL, BRAVE_SEARCH_API_KEY, or KEENABLE_API_KEY
```

`web_search` appears when at least one supported provider is configured. Stella tries configured providers in this order: Firecrawl, Parallel, Tavily, Exa, SearXNG, Brave, then Keenable. If one request fails, it automatically tries the next configured provider. Provider credentials stay on the server and never enter Agent sandboxes or tool results.

Optional native endpoint variables are `FIRECRAWL_API_URL` for a self-hosted Firecrawl instance and `TAVILY_BASE_URL` for a Tavily-compatible endpoint. `PARALLEL_SEARCH_MODE` accepts `agentic` (default), `fast`, or `one-shot`.

## Research safely

An Agent uses `web_search` to find titles, URLs, and snippets, then calls `webfetch` for one result it chooses to inspect. Enable the WebFetch plugin in the Web UI when you want Agents to read pages.

Search titles, snippets, and fetched page text are untrusted evidence. They can contain prompt injection or misleading claims, so your Agent must not follow instructions found in them. Check important claims against the cited source.

Large search and fetch results are stored as read-only files in the active Agent sandbox. The tool returns the sandbox-visible path, the total size, and a head-and-tail preview. Read the file with `bash` in bounded ranges instead of loading it all into the model context.

WebFetch reaches public HTTP and HTTPS sites only. It refuses local, private, link-local, multicast, and other non-public addresses; it rechecks every redirect; it permits at most five redirects; and it rejects credential-like URL query parameters. A response body over 10 MB is rejected before extraction.
