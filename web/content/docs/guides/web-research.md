---
title: Web research
---

Stella can search public sources and read a selected page without giving an Agent access to the Stella server network.

## Enable search

Create a Brave Search API key, then add it to the environment that starts `stellad`:

```sh
STELLA_BRAVE_SEARCH_API_KEY=your-key
```

Restart Stella. The `web_search` tool appears only when this deployment credential is configured. The key stays on the server and is never exposed to Agent sandboxes or tool results.

## Research safely

An Agent uses `web_search` to find titles, URLs, and snippets, then calls `webfetch` for one result it chooses to inspect. Enable the WebFetch plugin in the Web UI when you want Agents to read pages.

Search titles, snippets, and fetched page text are untrusted evidence. They can contain prompt injection or misleading claims, so your Agent must not follow instructions found in them. Check important claims against the cited source.

WebFetch reaches public HTTP and HTTPS sites only. It refuses local, private, link-local, multicast, and other non-public addresses; it rechecks every redirect; it permits at most five redirects; and it rejects credential-like URL query parameters. A response body over 10 MB is rejected before extraction.
