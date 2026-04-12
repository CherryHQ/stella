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

### EX-003: Agent Preset Loading

| Field | Value |
|-------|-------|
| **ID** | EX-003 |
| **Path** | `plugins/tools/agent/preset_loader.go:19-37` |
| **Access Type** | Filesystem |
| **Operations** | `os.UserHomeDir`, `os.Stat`, `os.ReadDir`, `os.ReadFile` |
| **Owner** | `plugins/tools/agent` |
| **Reason** | Loads agent preset configurations from filesystem at build/startup time. These are configuration files, not user data. |
| **Risk Level** | Low |
| **Closure Plan** | **PHASE 5** - Move to `sandbox.Host` mediated access or load via `internal/sandbox` config loader once Host supports config file reading. |

---

### EX-004: Skills Catalog Loading

| Field | Value |
|-------|-------|
| **ID** | EX-004 |
| **Path** | `plugins/tools/skills/catalog.go:28-52` |
| **Access Type** | Filesystem |
| **Operations** | `os.UserHomeDir`, `os.Stat`, `os.ReadDir`, `os.ReadFile` |
| **Owner** | `plugins/tools/skills` |
| **Reason** | Loads skill catalog metadata from user and workspace directories. Mixed config/data access. |
| **Risk Level** | Medium |
| **Closure Plan** | **PHASE 5** - Migrate to `sandbox.Host.ReadFile` for skill content loading. Catalog metadata may remain direct access if treated as config. |

---

### EX-005: System Prompt Loading (Runner)

| Field | Value |
|-------|-------|
| **ID** | EX-005 |
| **Path** | `internal/agent/runner/prompt.go:86-94` |
| **Access Type** | Filesystem |
| **Operations** | `os.ReadFile`, `os.ReadDir`, `os.Stat` |
| **Owner** | `internal/agent/runner` |
| **Reason** | Loads system prompt sections and skill files for prompt building. Happens at build/construction time. |
| **Risk Level** | Low |
| **Closure Plan** | **PHASE 5** - Determine if prompt building is build-time or execution-time. If execution-time, migrate to Host-mediated access. |

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
| *None yet* | - | - | - |

---

## Exception Audit Log

| Date | Action | Exception ID | Notes |
|------|--------|--------------|-------|
| 2026-04-12 | Created | EX-001 through EX-008 | Initial exceptions register for Phase 1 |

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
