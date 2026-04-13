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

### EX-003: Agent Preset Discovery Home Resolution Fallback

| Field | Value |
|-------|-------|
| **ID** | EX-003 |
| **Path** | `plugins/tools/agent/preset_loader.go`, `plugins/tools/agent/hostfs.go` |
| **Access Type** | Filesystem |
| **Operations** | `os.UserHomeDir`, fallback `os.Stat` / `os.ReadDir` / `os.ReadFile` when no host is injected |
| **Owner** | `plugins/tools/agent` |
| **Reason** | Phase 5 moved preset loading to host-first mediation during runner execution. Remaining direct access is limited to home-directory discovery and non-sandboxed/no-host callers. |
| **Risk Level** | Low |
| **Closure Plan** | **PHASE 6** - Introduce a host-backed/common config path service for home discovery or make all preset consumers pass a host/context explicitly. |

---

### EX-004: Skills Catalog Home Resolution Fallback

| Field | Value |
|-------|-------|
| **ID** | EX-004 |
| **Path** | `plugins/tools/skills/catalog.go`, `plugins/tools/skills/hostfs.go` |
| **Access Type** | Filesystem |
| **Operations** | `os.UserHomeDir`, fallback `os.Stat` / `os.ReadDir` / `os.ReadFile` when no host is injected |
| **Owner** | `plugins/tools/skills` |
| **Reason** | Phase 5 moved list/load/create/patch/remove and prompt-visible skill discovery to host-first mediation. Remaining direct access is limited to home-directory discovery and non-sandboxed/no-host callers. |
| **Risk Level** | Low |
| **Closure Plan** | **PHASE 6** - Replace home discovery with an explicit config/path service and remove filesystem fallbacks once all runtime callers inject a host. |

---

### EX-005: Prompt Context File Fallback

| Field | Value |
|-------|-------|
| **ID** | EX-005 |
| **Path** | `internal/agent/runner/prompt_host.go` |
| **Access Type** | Filesystem |
| **Operations** | fallback `os.ReadFile`, `os.ReadDir`, `os.Stat` when no host is injected |
| **Owner** | `internal/agent/runner` |
| **Reason** | Phase 5 moved AGENTS.md prompt-context loading to execution-time host mediation after session resolution. Remaining direct access only serves nil-host construction paths and tests. |
| **Risk Level** | Low |
| **Closure Plan** | **PHASE 6** - Remove nil-host fallback after all prompt construction paths run with an execution-time host. |

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
| 2026-04-13 | Updated | EX-003 through EX-005 | Narrowed from active runtime bypasses to host-first mediation with explicit nil-host fallbacks after Phase 5 work |

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
