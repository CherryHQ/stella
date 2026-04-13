# Phase 1: Exceptions Register

This document records all known direct `os` / `exec` / `net` / `http` bypasses that are not mediated by `sandbox.Host`. Each exception includes owner, reason, and closure plan.

## Exception Format

```
ID: EX-NNN
Path: file path and line numbers
Access Type: filesystem | process | network
Owner: responsible team/module
Reason: why this bypass exists
Risk Level: low | medium | high
Closure Plan: when/how this will be addressed
```

---

## Active Exceptions

### EX-001: Builtin Skills Extraction

| Field | Value |
|-------|-------|
| **ID** | EX-001 |
| **Path** | `internal/agent/runner/builtin/embed.go:16-42` |
| **Access Type** | Filesystem |
| **Operations** | `os.RemoveAll`, `os.MkdirAll`, `os.WriteFile` |
| **Owner** | `internal/agent/runner` |
| **Reason** | One-time extraction of embedded skill files at runner startup. Not per-tool execution; prepares filesystem for subsequent sandboxed operations. |
| **Risk Level** | Low |
| **Closure Plan** | **WONTFIX** - This is build-time/initialization code that prepares the workspace. It runs before any sandbox session exists and is not part of the tool execution boundary. |

---

### EX-002: Skills Builtin Embed Extraction

| Field | Value |
|-------|-------|
| **ID** | EX-002 |
| **Path** | `plugins/tools/skills/builtin/embed.go` (similar to EX-001) |
| **Access Type** | Filesystem |
| **Operations** | `os.RemoveAll`, `os.MkdirAll`, `os.WriteFile` |
| **Owner** | `plugins/tools/skills` |
| **Reason** | One-time extraction of builtin skills at startup. Prepares skill files for later sandboxed access. |
| **Risk Level** | Low |
| **Closure Plan** | **WONTFIX** - Initialization code, not tool execution. |

---

### EX-006: Workspace Creation (Factory)

| Field | Value |
|-------|-------|
| **ID** | EX-006 |
| **Path** | `internal/agent/workspace.go` (mkdir operations) |
| **Access Type** | Filesystem |
| **Operations** | `os.MkdirAll` |
| **Owner** | `internal/agent` |
| **Reason** | Creates workspace directories (skills, data, .agents) at session initialization. |
| **Risk Level** | Low |
| **Closure Plan** | **WONTFIX** - Workspace setup is infrastructure, not tool execution. Happens before sandbox session creation. |

---

### EX-007: Config Paths Resolution

| Field | Value |
|-------|-------|
| **ID** | EX-007 |
| **Path** | `internal/config/paths.go` |
| **Access Type** | Filesystem |
| **Operations** | `os.UserHomeDir`, `os.Getenv`, `filepath` operations |
| **Owner** | `internal/config` |
| **Reason** | Resolves configuration paths (AnnaHome, workspace). Configuration infrastructure. |
| **Risk Level** | Low |
| **Closure Plan** | **WONTFIX** - Configuration resolution is not part of sandbox execution boundary. |

---

### EX-008: Embedded Tools Extraction

| Field | Value |
|-------|-------|
| **ID** | EX-008 |
| **Path** | `internal/embedded/*.go` (inferred from usage) |
| **Access Type** | Filesystem |
| **Operations** | File extraction from embedded FS to disk |
| **Owner** | `internal/embedded` |
| **Reason** | Extracts embedded binaries (boxsh, etc.) to disk for execution. |
| **Risk Level** | Low |
| **Closure Plan** | **WONTFIX** - Binary extraction is installation/setup, not tool execution. |

---

### EX-009: MCP Remote HTTP/SSE Transport Dialing

| Field | Value |
|-------|-------|
| **ID** | EX-009 |
| **Path** | `plugins/tools/mcp/session.go` |
| **Access Type** | Network |
| **Operations** | `net/http` client creation and request transport for SSE / StreamableHTTP / HTTP |
| **Owner** | `plugins/tools/mcp` |
| **Reason** | Phase 5 moved MCP stdio process spawning onto `sandbox.Host.StartProcess`. Remote transports still dial directly because the managed MCP runtime is process-wide and not yet session-scoped to an execution-time host. |
| **Risk Level** | Medium |
| **Closure Plan** | **PHASE 6** - Introduce host-aware/network-mediated MCP runtime dialing or explicitly scope remote MCP transport as a separate non-sandboxed trust boundary in code and docs. |

---

## Future Exceptions (Expected)

The following will become exceptions once Phase 1 types are implemented:

| ID | Description | Expected Owner | Notes |
|----|-------------|----------------|-------|
| EX-100 | Boxsh binary validation | `internal/sandbox` | `exec.Command` for `boxsh --version` check |
| EX-101 | Boxsh process management | `internal/sandbox/boxshclient` | `exec.Command` for `boxsh --rpc` subprocess |

These are infrastructure processes (the sandbox backend itself), not user tool execution.

---

## Closed Exceptions (Historical)

| ID | Description | Closure Date | Resolution |
|----|-------------|--------------|------------|
| EX-003 | Agent Preset Discovery Home Resolution Fallback | 2026-04-13 | Removed `os.UserHomeDir` fallback from `LoadAgentPresets` and threaded explicit `HomeDir` from runner callers. |
| EX-004 | Skills Catalog Home Resolution Fallback | 2026-04-13 | Removed deprecated skills discovery wrapper and direct filesystem fallback; no-host callers now use an explicit relaxed local sandbox session. |
| EX-005 | Prompt Context File Fallback | 2026-04-13 | Removed direct filesystem fallback from `prompt_host.go`; prompt context now resolves through an injected host or an explicit relaxed local sandbox session. |

---

## Exception Audit Log

| Date | Action | Exception ID | Notes |
|------|--------|--------------|-------|
| 2026-04-12 | Created | EX-001 through EX-008 | Initial exceptions register for Phase 1 |
| 2026-04-13 | Updated | EX-003 through EX-005 | Narrowed from active runtime bypasses to host-first mediation with explicit nil-host fallbacks after Phase 5 work |
| 2026-04-13 | Closed | EX-003 | Removed preset home-directory fallback by threading explicit `HomeDir` into active preset discovery callers |
| 2026-04-13 | Closed | EX-004 | Removed deprecated skills discovery wrapper and direct filesystem fallback in favor of explicit config + relaxed local sandbox host resolution |
| 2026-04-13 | Closed | EX-005 | Removed prompt direct filesystem fallback in favor of injected host or explicit relaxed local sandbox session |
| 2026-04-13 | Added | EX-009 | Recorded remaining direct MCP remote transport dialing after stdio moved onto `sandbox.Host.StartProcess` |

---

## Review Checklist

Before adding a new exception:

- [ ] Can this be mediated by `sandbox.Host` instead?
- [ ] Is this truly build-time/initialization only?
- [ ] Is there a closure plan with a target phase?
- [ ] Has the risk level been assessed?
- [ ] Has an owner been assigned?

Before closing an exception:

- [ ] Has the code been migrated to Host-mediated access?
- [ ] Are tests updated?
- [ ] Is the closure documented with commit reference?
