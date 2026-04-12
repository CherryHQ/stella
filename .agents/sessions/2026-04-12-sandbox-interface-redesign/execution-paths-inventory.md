# Phase 1: Execution Paths Inventory and Classification

## Summary

This document inventories all filesystem, process, and network access paths in Anna's codebase, classifying each as mediated, to-be-mediated, or explicit exception.

## Built-in Core Tools (bash, read, write, edit)

### Current State

| Tool | Current Path | Classification | Notes |
|------|--------------|----------------|-------|
| **bash** | `plugins/tools/bash/bash.go` uses `exec.CommandContext` + `os.Environ()` | **TO-BE-MEDIATED** | Direct process spawning, no sandbox mediation |
| **read** | `plugins/tools/read/read.go` uses `os.Open`, `os.ReadFile` | **TO-BE-MEDIATED** | Direct filesystem access via `os` package |
| **write** | `plugins/tools/write/write.go` uses `os.MkdirAll`, `os.WriteFile` | **TO-BE-MEDIATED** | Direct filesystem access |
| **edit** | `plugins/tools/edit/edit.go` uses `os.ReadFile`, `os.WriteFile` | **TO-BE-MEDIATED** | Direct filesystem access |

### Boxsh-Backed Adapters (Current Mediation)

| Tool | Adapter Location | Classification | Notes |
|------|------------------|----------------|-------|
| **bash** | `internal/sandbox/boxshclient/tool_adapters.go` `BashAdapter` | **MEDIATED** | Uses `client.Exec` via boxsh RPC |
| **read** | `internal/sandbox/boxshclient/tool_adapters.go` `ReadAdapter` | **MEDIATED** | Uses `client.Read` via boxsh RPC |
| **write** | `internal/sandbox/boxshclient/tool_adapters.go` `WriteAdapter` | **MEDIATED** | Uses `client.Write` via boxsh RPC |
| **edit** | `internal/sandbox/boxshclient/tool_adapters.go` `EditAdapter` | **MEDIATED** | Uses `client.Edit` via boxsh RPC |

**Issue**: Dual path exists - boxsh adapters are used when backend available, but direct implementations remain. Need unification (Phase 4).

## Plugin Tools

### Path: `plugins/tools/`

| Tool | Filesystem | Process | Network | Classification |
|------|------------|---------|---------|----------------|
| **bash** | N/A (pure exec) | `exec.CommandContext` | N/A | **TO-BE-MEDIATED** |
| **read** | `os.Open`, `os.ReadFile` | N/A | N/A | **TO-BE-MEDIATED** |
| **write** | `os.MkdirAll`, `os.WriteFile` | N/A | N/A | **TO-BE-MEDIATED** |
| **edit** | `os.ReadFile`, `os.WriteFile` | N/A | N/A | **TO-BE-MEDIATED** |
| **webfetch** | N/A | N/A | `resty.Client` HTTP calls | **TO-BE-MEDIATED** |
| **notify** | N/A | N/A | Via `Notifier` interface | **TO-BE-MEDIATED** | Notification dispatch currently bypasses `sandbox.Host`; decide in Phase 5 whether it becomes host-mediated or an explicit exception |
| **skills** | `os.ReadFile`, `os.ReadDir`, `os.Stat`, `os.WriteFile`, `os.MkdirAll` | N/A | N/A (search uses external) | **TO-BE-MEDIATED** |
| **agent (subagent)** | N/A | N/A | N/A (delegates to runner) | **MEDIATED** (uses existing runner) |

### Sandbox Wrapper (Defense in Depth)

**Location**: `plugins/tools/sandbox/sandbox.go`

```go
type sandboxTool struct {
    inner      tools.Tool
    allowedDir string  // Path validation only, not true sandbox
    pathKey    string
}
```

**Classification**: **PARTIAL MEDIATION** - Path validation wrapper around native tool implementations. This is defense-in-depth, not the primary sandbox boundary.

## MCP-Related Execution Paths

### Path: `plugins/tools/mcp/`

| Component | Access Type | Implementation | Classification | Notes |
|-----------|-------------|----------------|----------------|-------|
| **session.go** | Process spawning | `exec.Command(server.Command, server.Args...)` for stdio transport | **TO-BE-MEDIATED** | MCP stdio transport spawns local processes |
| **session.go** | Network (HTTP) | `http.Client` for SSE/StreamableHTTP | **TO-BE-MEDIATED** | Outbound HTTP connections |
| **supervisor.go** | N/A | Lifecycle management | **N/A** | No direct resource access |
| **manager.go** | N/A | Tool registry, routing | **N/A** | No direct resource access |

**Transport Classes Used:**

1. **stdio**: `exec.Command` - spawns local MCP server processes
2. **SSE**: HTTP client - streaming HTTP connections
3. **StreamableHTTP**: HTTP client - request/response HTTP
4. **HTTP (legacy)**: HTTP client - basic HTTP transport

All local-side operations (process spawning, HTTP requests) must be mediated through `sandbox.Host`.

## Internal Execution Paths

### Path: `internal/agent/runner/`

| Component | Access Type | Implementation | Classification | Notes |
|-----------|-------------|----------------|----------------|-------|
| **builtin/embed.go** | Filesystem | `os.RemoveAll`, `os.MkdirAll`, `os.WriteFile` | **EXCEPTION** | One-time extraction at startup, not per-tool |
| **gorunner.go** | Process | `os.Getenv("PATH")`, `os.Stat` for sandbox setup | **TO-BE-MEDIATED** | Backend initialization only |
| **prompt.go** | Filesystem | `os.ReadFile`, `os.ReadDir`, `os.Stat` | **EXCEPTION (EX-005)** | System prompt loading at construction/build time; revisit in Phase 5 |

### Path: `internal/sandbox/`

| Component | Access Type | Implementation | Classification | Notes |
|-----------|-------------|----------------|----------------|-------|
| **boxsh.go** | Process | `exec.CommandContext` for boxsh validation | **MEDIATED** | Sandbox backend process management |
| **boxshclient/client.go** | Process | `exec.Command` for boxsh --rpc | **MEDIATED** | Core sandbox subprocess |
| **boxshclient/*.go** | Various | All boxsh RPC calls | **MEDIATED** | All mediated through boxsh |

### Path: `plugins/tools/skills/`

| Component | Access Type | Implementation | Classification | Notes |
|-----------|-------------|----------------|----------------|-------|
| **catalog.go** | Filesystem | `os.UserHomeDir`, `os.Stat`, `os.ReadDir`, `os.ReadFile` | **EXCEPTION (EX-004)** | Skill catalog metadata loading; revisit in Phase 5 |
| **manage.go** | Filesystem | `os.Stat`, `os.ReadFile` | **TO-BE-MEDIATED** | Skill installation |
| **atomicwrite.go** | Filesystem | `os.MkdirAll`, `os.CreateTemp`, `os.Remove`, `os.Rename` | **TO-BE-MEDIATED** | Atomic file operations |
| **tool.go** | Filesystem | `os.ReadFile` | **TO-BE-MEDIATED** | Skill content loading |
| **builtin/embed.go** | Filesystem | `os.RemoveAll`, `os.MkdirAll`, `os.WriteFile` | **EXCEPTION** | One-time extraction |

### Path: `plugins/tools/agent/`

| Component | Access Type | Implementation | Classification | Notes |
|-----------|-------------|----------------|----------------|-------|
| **preset_loader.go** | Filesystem | `os.UserHomeDir`, `os.Stat`, `os.ReadDir`, `os.ReadFile` | **EXCEPTION (EX-003)** | Preset/config loading at construction time; revisit in Phase 5 |

## Summary by Category

### Filesystem Access

| Source | Count | Mediated | To-Be-Mediated | Exception |
|--------|-------|----------|----------------|-----------|
| Core tools (plugin) | 4 | 0 | 4 | 0 |
| Boxsh adapters | 4 | 4 | 0 | 0 |
| Skills tools | 15+ | 0 | 11+ | 4 |
| MCP (stdio) | 1 | 0 | 1 | 0 |
| Runner/internal | 8+ | 0 | 2+ | 6+ |
| **Total** | **32+** | **4** | **18+** | **8+** |

### Process Access

| Source | Count | Mediated | To-Be-Mediated | Exception |
|--------|-------|----------|----------------|-----------|
| bash tool | 1 | 0 | 1 | 0 |
| boxsh backend | 2 | 2 | 0 | 0 |
| MCP stdio | 1 | 0 | 1 | 0 |
| **Total** | **4** | **2** | **2** | **0** |

### Network Access

| Source | Count | Mediated | To-Bemediated | Exception |
|--------|-------|----------|---------------|-----------|
| webfetch | 1 | 0 | 1 | 0 |
| MCP (HTTP transports) | 1 | 0 | 1 | 0 |
| notify | 1 | 0 | 1 | 0 |
| **Total** | **3** | **0** | **3** | **0** |

## Classification Definitions

### Mediated
Access goes through `sandbox.Host` or equivalent constrained interface. Operations respect sandbox policy.

### To-Be-Mediated
Direct use of `os`, `exec`, `net/http` packages. Must be migrated to use `sandbox.Host` methods.

### Explicit Exception
Known direct access that is:
1. One-time initialization or build/construction-time only, or
2. Explicitly documented in `exceptions-register.md` with an exception ID, owner, and closure plan, and
3. Not treated as mediated until explicitly migrated

## Migration Priority

1. **P0 (Critical)**: Core tools (bash, read, write, edit) - duplicative paths exist, need unification
2. **P1 (High)**: MCP stdio transport - process spawning needs mediation
3. **P2 (Medium)**: Plugin tools (webfetch HTTP, skills filesystem)
4. **P3 (Low)**: Internal runner paths (prompt loading, preset loading)
