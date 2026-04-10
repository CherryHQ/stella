# boxsh Sandbox Exploration

Date: 2026-04-10  
Branch: `feat/explore-boxsh-sandbox`  
Upstream: https://github.com/xicilion/boxsh

---

## Summary

boxsh is a strong candidate for replacing Anna's current path-guard tool backend with a real process sandbox backend for local code execution.

The right integration shape is:

- ship `boxsh` as a **prebuilt binary**, using the same binary-management pattern Anna already uses for external tools like `rtk`
- integrate it at the **core tool boundary**, not by sandboxing the whole Anna process
- make it the **required backend on Linux and macOS**
- keep **Windows** on the current behavior
- treat **macOS** as a weaker implementation than Linux and document the limits explicitly

This is promising enough to proceed to implementation.

---

## Current State in Anna

Relevant files reviewed:

- `plugins/tools/bash/bash.go`
- `plugins/tools/read/read.go`
- `plugins/tools/write/write.go`
- `plugins/tools/edit/edit.go`
- `plugins/tools/sandbox/sandbox.go`
- `internal/agent/runner/gorunner.go`

Anna's current protection model is not OS-level sandboxing. It is a narrow tool backend constraint.

| Tool | Current behavior | Actual protection level | Main gap |
|---|---|---:|---|
| `read` | validates `file_path` is under `UserDataDir` | path guard only | no process isolation |
| `write` | validates `file_path` is under `UserDataDir` | path guard only | writes still hit host filesystem directly |
| `edit` | validates `file_path` is under `UserDataDir` | path guard only | edits still hit host filesystem directly |
| `bash` | runs in `UserDataDir` when present | working directory scoping only | shell can still use absolute paths, env, network, inherited host view |

So the current backend is best described as:

- **existing path-guard backend** for file tools
- **cwd-scoped shell execution** for `bash`

It should not be described as a strong sandbox.

---

## Why boxsh Fits

boxsh is attractive because it already models the same four primitive operations Anna exposes today:

- `bash`
- `read`
- `write`
- `edit`

That means we can swap the **tool backend** without redesigning the runner loop or changing the LLM-facing tool surface.

What boxsh adds that Anna currently lacks:

| Capability | Anna today | boxsh |
|---|---|---|
| Shell process isolation | no | yes |
| Filesystem isolation | no | yes, platform-dependent |
| Copy-on-write workspace | no | yes |
| Network restriction | no | yes on Linux; policy-based limits on macOS |
| Unified shell + file tool backend | no | yes |
| stdio RPC protocol | no | yes |

This is the main architectural reason to pursue it: the integration seam is clean.

---

## Recommended Distribution Strategy

We should **not** build boxsh from source as part of Anna setup.

We should ship it the same way Anna already manages external helper binaries like `rtk`:

- download a prebuilt release binary by OS/arch
- verify checksum if upstream provides one
- cache/version it through Anna's existing binary-management path
- invoke that binary from the Go runtime

Do **not** introduce a separate bespoke install convention unless the existing helper-binary flow cannot support it.

### Why this is the right tradeoff

- avoids adding a CMake/C++ toolchain requirement to users and CI
- matches Anna's existing operational model for auxiliary binaries
- makes version pinning explicit
- keeps upgrades reversible

### Fallback behavior

If boxsh is configured but the binary is unavailable for the current platform or fails validation, the behavior should be explicit.

Recommended rollout behavior:

- if backend is `boxsh` and binary is unavailable: **fail closed for that backend selection**, with a clear error
- if backend is unspecified/default: continue using the **existing path-guard backend**

That avoids silent security downgrades.

---

## Integration Boundary

The correct integration seam is the core tool builder in:

- `internal/agent/runner/gorunner.go`

The intended architecture is:

1. Anna chooses a tool backend
2. core tools are constructed from that backend
3. both backends preserve the same external tool names: `bash`, `read`, `write`, `edit`

### Proposed internal package

Create a thin adapter package, for example:

- `internal/sandbox/boxshclient/`

Responsibilities:

- locate/download the boxsh binary via Anna's existing helper-binary mechanism
- start `boxsh --rpc ...`
- send and receive stdio JSON-RPC
- expose Go methods for `Exec`, `Read`, `Write`, `Edit`
- normalize boxsh responses into Anna's tool contract
- own subprocess lifecycle and cleanup

Sketch:

```go
type Client struct {
    cmd    *exec.Cmd
    stdin  io.Writer
    stdout io.Reader
}

func (c *Client) Exec(ctx context.Context, req ExecRequest) (*ExecResult, error)
func (c *Client) Read(ctx context.Context, path string, offset, limit int) (string, error)
func (c *Client) Write(ctx context.Context, path, content string) error
func (c *Client) Edit(ctx context.Context, path string, edits []Edit) (*EditResult, error)
func (c *Client) Close() error
```

### Backend switch

The doc previously suggested `sandbox.backend = "native" | "boxsh"`, but `native` is imprecise.

Use clearer terminology:

- `path_guard` — current behavior
- `boxsh` — new backend

Current plan decision:

- Linux and macOS require `boxsh`
- Windows keeps the current `path_guard`/cwd-scoped backend
- there is no backend-selection toggle in phase 1

---

## Tool Contract Mapping

boxsh is MCP/JSON-RPC-first. Anna tools are Go interfaces returning text plus error.

That mismatch is manageable but should be stated clearly.

| Anna contract | boxsh contract | Adapter action |
|---|---|---|
| `file_path` field | `path` field | rename field |
| `edit` currently modeled as single replacement in Anna tool API | boxsh supports edit arrays | wrap single edit in one-element array, keep adapter flexible for future multi-edit support |
| text result + Go error | `content`, `structuredContent`, `isError` | flatten successful text output, map `isError` to Go error |
| `bash` appends footer metadata today | boxsh returns structured fields | decide whether to preserve current footer format or normalize Anna's native backend later |

The adapter should preserve existing LLM-facing behavior initially unless there is a deliberate tool contract cleanup.

---

## Workspace and COW Model

This is the most important design detail.

The previous draft was too vague. The integration should define exact source and destination semantics.

### Recommended phase-1 model

For a given agent execution context:

- **source (`SRC`)**: the current `UserDataDir`
- **destination (`DST`)**: a per-session upper/workspace directory managed by Anna

This gives the boxsh backend a copy-on-write view rooted in the same user workspace Anna already uses.

### Why `UserDataDir` should be the source

Today Anna's file tools and shell behavior already assume `UserDataDir` is the effective writable workspace boundary. Reusing that as `SRC` minimizes semantic drift.

Using repo root or arbitrary `WorkDir` as the source would change behavior more aggressively and introduce new policy questions too early.

### Destination semantics

Phase 1 should use:

- **ephemeral per-session `DST`**

Reasons:

- safer default
- clearer isolation story
- easier cleanup
- easier testing
- avoids surprising persistence differences between path-guard and boxsh backends while the model is still being validated

Open question for later phases:

- whether some agents should use **persistent** COW upperdirs across sessions

That should be deferred until the basic backend works.

### Expected behavior in phase 1

- reads see the `UserDataDir` content through the COW view
- writes and edits land in session-local upper storage
- shell commands observe the same COW view as file tools
- when the session ends, Anna decides whether to discard or preserve the upperdir for debugging

This is a meaningful semantic change from the current path-guard backend, so it should be opt-in at first.

---

## Process Model and Lifecycle

The client should use a **long-lived boxsh subprocess** per runner instance, not spawn one process per tool call.

Why:

- avoids repeated startup cost
- matches boxsh's own RPC/worker design
- reduces process churn
- gives one consistent backend per agent execution context

### Recommended lifecycle

- create boxsh client when the tool backend is constructed
- reuse that process for all tool calls in the runner lifetime
- terminate on runner close
- if the boxsh subprocess crashes, surface the error and recreate only if the caller explicitly retries through normal runner lifecycle

### Concurrency note

boxsh already has internal worker pooling. Anna should avoid adding unnecessary extra pooling around it before measuring actual need.

Phase 1 should optimize for correctness and debuggability, not maximum parallel throughput.

---

## Platform Caveats

### Linux

Linux is the primary target for this backend.

Expected advantages:

- user/mount namespace isolation
- copy-on-write overlay workspace
- network namespace support

Operational caveat:

- some filesystems/kernel combinations may require `fuse-overlayfs` fallback

That should be documented as an operational dependency, not discovered accidentally by users.

### macOS

macOS should be treated as **experimental/weaker** in phase 1.

From upstream docs/tests/source review, the macOS backend relies on:

- Seatbelt via `sandbox_init`
- SBPL policy rules
- `clonefile(2)` on APFS for COW behavior

Important caution:

- upstream tests indicate weaker filesystem isolation semantics on macOS than on Linux
- in particular, behavior around `/tmp` and host visibility should be treated as needing empirical validation in Anna's invocation model

So the correct statement is not "macOS is secure enough because documented," but:

- **macOS may still be operationally useful as a constrained backend, but its guarantees must be documented precisely after validation**

### Platform comparison

| Feature | Linux | macOS |
|---|---|---|
| process isolation strength | stronger | weaker |
| mount namespace isolation | yes | no |
| COW implementation | overlayfs / fuse-overlayfs fallback | clonefile on APFS |
| network isolation | namespace-based | policy-based, not namespace-equivalent |
| rollout recommendation | opt-in, likely first | opt-in experimental |

---

## Non-Goals

To avoid scope creep, phase 1 should explicitly **not** try to do these things:

- sandbox the whole Anna server process
- sandbox database access or admin APIs
- redesign plugin execution around boxsh
- guarantee Linux-equivalent isolation on macOS
- replace every filesystem access path in Anna outside the core tool boundary

This is a **tool backend** project first.

---

## Decisions Still Needed

These are the real product/engineering decisions that remain open:

1. **Configuration scope**
   - decided: per-agent sandbox policy only

2. **Session persistence**
   - ephemeral COW upperdir only
   - optional persistent upperdir later

3. **Network policy**
   - disable by default on Linux?
   - what is the macOS equivalent policy?

4. **Failure policy**
   - Linux/macOS should fail closed when the managed `boxsh` binary or sandbox prerequisites are invalid
   - Windows should stay on the current backend

5. **Result formatting**
   - preserve current `bash` footer formatting exactly
   - or normalize both backends later

---

## Recommended Rollout Plan

### Phase 1 — binary bootstrap and smoke test

- add boxsh binary acquisition through Anna's existing helper-binary flow
- pin a tested version
- verify basic process startup on supported Linux/macOS targets
- run `boxsh --rpc` health checks

### Phase 2 — thin JSON-RPC client

- implement `internal/sandbox/boxshclient`
- add request/response handling
- map `bash`, `read`, `write`, `edit`
- add robust process cleanup and error propagation

### Phase 3 — opt-in backend wiring

- add backend selection: `path_guard` or `boxsh`
- keep default as `path_guard`
- allow per-agent opt-in to `boxsh`
- use `UserDataDir` as `SRC` and ephemeral session upperdir as `DST`

### Phase 4 — integration tests

Linux:

- verify shell cannot mutate host files outside intended COW workspace
- verify all four tools see the same view
- verify timeout and cleanup behavior

macOS:

- document tested guarantees instead of assuming Linux parity
- verify APFS/Seatbelt behavior in Anna's actual invocation pattern

### Phase 5 — rollout decision

After Linux integration tests and real usage:

- decide whether Linux should remain opt-in or become default
- keep macOS experimental until its guarantees are well characterized

---

## Verdict

Proceed.

Not because boxsh is generically "more secure," but because it matches Anna's existing tool boundary almost exactly while adding the missing isolation model underneath.

The main tradeoffs are:

- adopting a subprocess-backed RPC adapter
- accepting a semantic change toward COW workspaces for opted-in sessions
- handling Linux and macOS as different security envelopes instead of pretending they are the same

That is an acceptable trade for an opt-in first rollout.
