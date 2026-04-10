# Tasks: boxsh sandbox backend

- [x] Phase 1.1 Extend managed binary download/extraction flow to include `boxsh`
- [x] Phase 1.2 Add managed-only boxsh binary resolution helpers and tests
- [x] Phase 1.3 Add Linux/macOS startup preflight for binary/platform/filesystem/network validation
- [x] Phase 1.4 Add per-agent sandbox network policy config types and snapshot loading
- [x] Phase 1.5 Update exploration/docs for mandatory Linux/macOS boxsh usage and Windows skip path

- [x] Phase 2.1 Implement `internal/sandbox/boxshclient/` process lifecycle and JSON-RPC transport
- [x] Phase 2.2 Implement tool methods for `Exec`, `Read`, `Write`, `Edit`
- [x] Phase 2.3 Implement shared backend construction so all four tools use one boxsh session
- [x] Phase 2.4 Implement session workspace helpers for user-session and non-user-session sandbox roots plus ephemeral session `DST`
- [x] Phase 2.5 Normalize boxsh responses into Anna-compatible tool results/errors

- [x] Phase 3.1 Introduce boxsh-backed adapters for `bash`, `read`, `write`, and `edit`
- [x] Phase 3.2 Wire Linux/macOS core tools through boxsh in `gorunner.go`
- [x] Phase 3.3 Propagate boxsh health into runner liveness
- [x] Phase 3.4 Preserve Windows current backend behavior
- [x] Phase 3.5 Ensure runner/tool cleanup closes boxsh and removes ephemeral session upperdirs
- [x] Phase 3.6 Remove redundant Linux/macOS reliance on path-guard wrappers where superseded

- [x] Phase 4.1 Add integration tests for shared COW view across all four tools
- [x] Phase 4.2 Add isolation tests for cross-workspace denial
- [x] Phase 4.3 Add network policy tests for disabled / allow_all / whitelist
- [x] Phase 4.4 Document Linux/macOS guarantees and limitations
- [ ] Phase 4.5 Update README and user-facing docs
