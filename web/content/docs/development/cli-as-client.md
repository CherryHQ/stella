---
title: CLI and native agent tools
---

> This section is for developers contributing to Stella.

## Overview

`stellad server` is the only process that opens PostgreSQL, writes server-owned
files, fetches feeds, or mutates Stella state. Human surfaces call the server over
HTTP; agent surfaces call native built-in tools that run server-side with an
`authz.Identity` facade.

The old sandbox pattern is gone: the sandbox image does not ship a Stella CLI,
and Stella no longer injects scoped bearer tokens into agent sessions. Agents cannot
authenticate to the HTTP API from inside the sandbox. Give agent capabilities as
native tools instead.

```
┌──────────┐         HTTP          ┌──────────────────────┐
│   Web UI │ ────────────────────▶ │   stellad server     │
└──────────┘                       │  • PostgreSQL        │
┌──────────┐         HTTP          │  • scheduler         │
│HTTP client│ ────────────────────▶ │  • plugin host       │
└──────────┘                       │  • tool handlers     │
┌──────────┐   native tool call    │  • authz.Identity    │
│  Agent   │ ────────────────────▶ │    As-facades        │
└──────────┘                       └──────────────────────┘
```

## Why

- **One source of truth.** Business rules live in the server, not duplicated
  across CLI, Web UI, and agent code.
- **Least authority.** Agents receive only the tools registered for their role;
  they do not get a general bearer token or a CLI escape hatch.
- **Auditability.** Mutations still pass through server handlers, where logging,
  metrics, rate limiting, and authorization are centralized.
- **Typed agent APIs.** Tool schemas describe exactly what agents may call and
  what arguments are valid.

## Pattern

For a new agent capability:

1. Implement the server-side domain handler and authorization checks.
2. Expose the agent surface as a native tool action, usually with `x-agent-tool`
   metadata in the OpenAPI spec and generated glue from toolgen.
3. Build the tool with an `authz.Identity` facade such as `identity.AsUser()` or
   the domain-specific equivalent, rather than accepting caller-supplied subject
   overrides.
4. Update the relevant system skill so agents use the tool name and action fields,
   not a shell command.

`stellad` subcommands remain for operators and local maintenance, but they are
not an agent integration surface.

## What commands must NOT do

- Open PostgreSQL via `internal/db.OpenDB`.
- Construct domain stores like `recally.NewStore(db)`.
- Read or write server-owned files under `STELLA_HOME` directly.
- Add sandbox-only authentication paths or depend on agent-scoped bearer tokens.

A grep over non-server command packages for `OpenDB`, `sqlc.`, or
`NewFileManager` should turn up empty. Server bootstrap and intentionally local
maintenance paths belong under `cmd/stellad/` or internal service packages.
