---
title: MCP Servers
---

## What MCP servers do

Stella connects agents to external [Model Context Protocol](https://modelcontextprotocol.io)
servers and exposes their tools to the active turn. Declarations are ordinary
files. A standalone server uses `mcp/<name>.json`; a server inside a package is
part of that package's complete file tree.

Stella is an MCP client over HTTP transports only:

- `streamable_http`, the streamable HTTP transport.
- `sse`, HTTP plus Server-Sent Events.

Local `stdio` servers are not supported. Stella does not start a local process
for a model-facing MCP declaration. Endpoints must resolve to public addresses;
loopback and private URLs require `STELLA_MCP_ALLOW_PRIVATE_ENDPOINTS=1` for
local development.

## Scopes and selection

MCP files use the same four scopes as Skills:

| Scope          | Visible to              |
| -------------- | ----------------------- |
| `system`       | every user and Agent    |
| `system_agent` | every user of one Agent |
| `user`         | one user's Agents       |
| `user_agent`   | one user and one Agent  |

For a package, Stella selects the most specific complete package from the four
typed roots in this order:

```
user_agent > user > system_agent > system
```

The selected package replaces the complete package at broader scopes. Its MCP
servers, Skills, CLI entries, and environment declarations are resolved
together. An independent copy has no parent link and does not follow later
source edits. A standalone MCP file is selected by its name and does not merge
with a package declaration of the same name.

A `settings.json` entry can disable a server or apply administrator-only
`forbidden` and `disabled_tools` limits. A disabled winner masks broader
resources. A personal setting cannot override an administrator prohibition.

Stella captures declarations at turn admission. Editing a file during a turn
applies on the next turn; the current turn keeps its endpoint, tool catalog, and
credential references. A catalog refresh observes the current file on a later
turn and is not a separate registration record.

## Authentication

A declaration stores only a credential reference and public connection fields.
Bearer tokens, OAuth tokens, refresh tokens, and client secrets are encrypted in
the credential store. A server with no authentication needs no secret and works
without credential storage configured.

The credential mode can be `shared` for a system-owned declaration or `per_user`
for separate user grants. User and user-agent declarations cannot request a
shared credential. The declaration's scope and credential mode determine which
Vault tuple is used; another user's grant is never a fallback.

## OAuth connections

For an OAuth 2.1 server, choose **Connect** for the current file declaration.
Stella discovers the authorization server, performs Authorization Code with
PKCE, and stores the resulting grant separately from the file. The redirect URI
is `<STELLA_BASE_URL>/api/mcp/oauth/callback` and must be reachable by the
browser.

**Disconnect** is an explicit local authorization action. It revokes the
matching stored grant, rotates its generation, closes the matching connection,
and blocks late callbacks or refreshes; remote provider revocation is not
guaranteed. Deleting or editing the MCP file does not perform Disconnect.
Editing the endpoint or other authentication identity produces a new target and
never reuses the old grant; ordinary Skill or package content edits keep it.

## Status and probing

Probe reads the current declaration and performs one remote discovery. The
result is an observation for the current target and credential owner. It is not
a persisted MCP registration or an installation record. A later turn may probe
again when the catalog is absent or stale.

| Status       | Meaning                                                  |
| ------------ | -------------------------------------------------------- |
| `unknown`    | no successful discovery is available for this target     |
| `ok`         | the last discovery listed tools                          |
| `error`      | the endpoint or discovery failed; the reason is redacted |
| `needs_auth` | the server rejected the matching credential              |

A failed probe does not hide sibling servers or Skills in the same package. A
server's tool catalog is an observation that later turns may reuse or refresh;
the MCP connection itself belongs to the current session and closes with it.

## Tool permissions

Each exported remote tool can be enabled or disabled at the four scopes. An
administrator prohibition wins over a personal enable. The package enable state
also controls every MCP declaration and Skill in that package. A tool switch
cannot enable a disabled package.

Native tools are separate system capabilities. A package or MCP server with the
same display name cannot grant a Native capability, and disabling an MCP file
does not disable a Native route.

## Marketplace

The Web UI can browse the official [MCP Registry](https://registry.modelcontextprotocol.io)
and copy a supported HTTP declaration into a writable scope. Marketplace data
provides a starting declaration only. You still review its endpoint,
authentication type, credential reference, and scope before saving. Registry
metadata does not create a parent registration or an automatic update link.

## Editing files

Use the MCP page or the file API to create, read, edit, copy, or delete a
standalone declaration. Package MCP servers are edited through the package file
view. Complete package copies are independent. Content conflicts are reported
when an edit uses a stale digest; direct shell writes remain nontransactional.

Deleting a declaration removes it from future selection. It does not revoke its
OAuth grant or erase retained resource bytes immediately. Disconnect the target
before deleting it when local access must be revoked; remote provider
revocation is not guaranteed.

## Troubleshooting

| Symptom                    | Meaning                                  | Fix                                                      |
| -------------------------- | ---------------------------------------- | -------------------------------------------------------- |
| `needs_auth`               | the matching grant was rejected          | reconnect OAuth or update the bearer secret, then probe  |
| `error`                    | the endpoint or discovery failed         | check the endpoint from the server and try the next turn |
| a server is missing        | a narrower scope disabled or replaced it | inspect the winning scope and `settings.json`            |
| a tool is missing          | the current catalog does not contain it  | edit the declaration or wait for the next discovery      |
| local `stdio` does not run | local process transports are unsupported | expose the server over an allowed HTTP transport         |
