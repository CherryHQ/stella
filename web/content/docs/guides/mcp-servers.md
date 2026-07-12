---
title: MCP Servers
---

## What MCP Servers Do

Stella can connect to external [Model Context Protocol](https://modelcontextprotocol.io) servers and expose their tools to your agents. Register a server once and its tools appear in the agent's toolbox, namespaced as `mcp__<server>__<tool>` so they never collide with skills or built-in tools.

Stella is an MCP **client** over HTTP-based transports only:

- `streamable_http` — the streamable HTTP transport (default).
- `sse` — HTTP + Server-Sent Events.

Local `stdio` servers are intentionally not supported: the multi-user sandbox never spawns local processes.

## Scopes

Registrations use the same four scopes as skills and the vault, so a server can be shared or private:

| Scope          | Visible to                       |
| -------------- | -------------------------------- |
| `system`       | every agent, every user          |
| `system_agent` | one agent, across all users      |
| `user`         | one user, across their agents    |
| `user_agent`   | one user with one specific agent |

When two registrations share a name, the most specific wins: `user_agent` > `user` > `system_agent` > `system`.

## Authentication

A server may need a bearer token. Pass it with `--auth bearer --token <token>`; the token is stored **encrypted in the vault** under the same scope as the registration (see [Secrets and Keys](/docs/guides/secrets-and-keys)) and is never written to the registration table. Servers that need no auth use `--auth none` (the default) and work even without the vault configured.

> Full interactive OAuth authorization for MCP servers is not yet implemented. For an OAuth-protected server, obtain a token out of band and store it as a bearer credential.

## Managing Servers

Manage MCP servers from the Web UI. Open the agent or workspace settings, add the server URL, choose its scope, and provide a bearer token when the server requires one.

The same operations are available over the HTTP API under `/api/mcp/servers`.
