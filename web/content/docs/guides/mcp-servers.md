---
title: MCP Servers
---

## What MCP Servers Do

Stella can connect to external [Model Context Protocol](https://modelcontextprotocol.io) servers and expose their tools to your agents. Register a server once and its tools appear in the agent's toolbox, namespaced as `mcp__<server>__<tool>` so they never collide with skills or built-in tools.

Stella is an MCP **client** over HTTP-based transports only:

- `streamable_http` — the streamable HTTP transport (default).
- `sse` — HTTP + Server-Sent Events.

Local `stdio` servers are intentionally not supported: the multi-user sandbox never spawns local processes.

Endpoints must resolve to public addresses. Loopback and private-network URLs are refused unless the operator starts `stellad` with `STELLA_MCP_ALLOW_PRIVATE_ENDPOINTS=1`, which is meant for local development servers.

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

A server may need a bearer token. Configure it when creating or editing the registration in the Web UI, or pass a `token` in the request body of `POST /api/mcp/servers`; the token is stored **encrypted in the vault** under the same scope as the registration (see [Secrets and Keys](/docs/guides/secrets-and-keys)) and is never written to the registration table. Servers that need no auth need no credential and work even without the vault configured.

> Full interactive OAuth authorization for MCP servers is not yet implemented. For an OAuth-protected server, obtain a token out of band and store it as a bearer credential.

## Status and probing

Stella probes each registered server — connects and fetches its tool list — and records the result on the registration:

| Status       | Meaning                                                          |
| ------------ | ---------------------------------------------------------------- |
| `unknown`    | Not probed yet                                                   |
| `ok`         | The last probe connected and listed tools                        |
| `error`      | The last probe or tool call failed; the redacted reason is shown |
| `needs_auth` | The server rejected the stored credential with 401/403           |

A probe runs automatically when you create a server, when its URL, transport, or auth changes, and when an agent session needs the tool list and the last snapshot is older than 24 hours. You can also trigger one any time with **Probe** in the Web UI, or with `POST /api/mcp/servers/{id}/probe` from the API. A failed probe never breaks anything — it just updates the status so you can see the problem (and the redacted reason) in the UI.

When a tool call is rejected with 401/403, the server moves to `needs_auth`; update the credential in the Web UI and probe again.

## Per-tool permissions

Every tool a server exposes can be switched on or off individually, using the same four-scope override model as every other tool:

| Scope          | Who it applies to                               |
| -------------- | ----------------------------------------------- |
| `user_agent`   | you, for one specific agent (most specific)     |
| `user`         | you, across all your agents                     |
| `system_agent` | the agent, for every user (administrators only) |
| `system`       | the whole deployment (administrators only)      |

An administrator's **disable** always wins over a user's enable; otherwise the more user-specific layer wins. Switch a tool in **Personal Settings → Agents → Tools** (or the admin console for system scopes), or with `PATCH /api/agents/{id}/tools/{toolName}`.

The server's **enable switch is separate**: it turns the whole registration on or off. While a server is disabled, unreachable, or rejecting credentials, its tools stay listed but their switches have no effect until the server is healthy again — the header shows why.

Because overrides are keyed by tool name (`mcp__<server>__<tool>`), renaming a server migrates its tools' overrides to the new prefix automatically, and deleting a server removes them. Both only happen once no other registration in any scope still uses that name. If two registrations share a name in different scopes, an override applies to whichever registration wins for the context.

## Managing Servers

Manage personal `user` and `user_agent` registrations from **Personal Settings → MCP Servers**. Administrators manage deployment-owned `system` and `system_agent` registrations from **Admin Console → Deployment resources → Global MCP**. Add the server URL, choose whether it applies to every agent or one agent, and provide a bearer token when required. There is no MCP management CLI: management happens in the Web UI, the HTTP API under `/api/mcp/servers`, or through the agent's `settings_mcp_server_*` tools.
