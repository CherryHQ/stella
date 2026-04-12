# Phase 4 core tools parity matrix

Status: complete

This matrix records the unified host-backed behavior for the built-in core tools after Phase 4.

| Tool | Parity area | Unified behavior | Validation |
| --- | --- | --- | --- |
| `bash` | working directory | Executes from the session host working directory unless overridden by host policy | `internal/agent/runner/gorunner_test.go`, `internal/sandbox/tools_test.go` |
| `bash` | PATH/tool bin prefix | Prepends `ToolsBinDir` to `PATH` before execution | `internal/sandbox/tools_test.go`, runner boxsh integration |
| `bash` | exit/timing footer | Returns normalized `stdout`/`stderr` content with `[exit:N | duration]` footer | `internal/sandbox/tools.go`, `internal/sandbox/tools_test.go` |
| `read` | path handling | Uses `Host.ReadFile` and host path resolution; caller-facing API remains `file_path` | `internal/sandbox/tools.go`, `internal/sandbox/boxshclient/*_test.go` |
| `read` | pagination semantics | Interprets `offset` / `limit` as 1-based line pagination at the tool layer | `internal/sandbox/tools_test.go` |
| `read` | truncation hints | Uses `tools.TruncateHead` and emits `[Use offset=N to continue reading]` when additional lines remain | `internal/sandbox/tools.go`, `internal/sandbox/tools_test.go` |
| `read` | binary detection | Rejects binary files with the same guidance as the legacy direct tool | `internal/sandbox/tools_test.go` |
| `write` | parent directory creation | Creates parent directories before writing, matching legacy direct tool behavior | `internal/sandbox/tools.go`, `internal/sandbox/tools_test.go` |
| `write` | result shaping | Returns `Wrote <path> (<bytes> bytes)` | `internal/sandbox/tools.go`, `internal/sandbox/tools_test.go` |
| `edit` | uniqueness enforcement | Fails when `old_string` is missing or matches more than once | `internal/sandbox/tools.go`, `internal/sandbox/tools_test.go` |
| `edit` | mutation path | Performs edits through `Host.EditFile` after tool-layer preflight checks | `internal/sandbox/tools.go`, `internal/sandbox/tools_test.go` |
| all | backend path | Same host-backed tool implementations are used for boxsh and relaxed/local sessions; noop still falls back to native tools because it has no session host | `internal/agent/runner/coretools_builder.go`, `internal/agent/runner/coretools_builder_test.go` |
| all | boxsh adapter removal | `internal/sandbox/boxshclient/tool_adapters.go` removed; backend tests now validate raw client/session behavior instead of duplicate tool wrappers | `internal/sandbox/boxshclient/*_test.go` |
