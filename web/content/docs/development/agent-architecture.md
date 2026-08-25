---
title: Agent architecture
---

> This page is for developers changing Stella's agent runtime, sessions, memory, channels, scheduler, internal delegate adapter, or task system.

Stella's agent architecture is split around one rule:

**Callers express business intent; `agent.Service` chooses session policy; `session.Registry` validates the session; `runtime.Runtime` runs the turn; `memory.Provider` stores and assembles content.**

```text
channel / server / scheduler / task / delegate
        |
        v
internal/agent.Service        business intent seam
        |
        +--> internal/agent/session.Registry   session lifecycle and policy
        |
        +--> internal/agent/runtime.Runtime    runner cache and turn execution
                 |
                 v
              internal/memory.Provider         messages, summaries, profile, snapshots
```

The old `Pool` shape mixed these responsibilities. New code should not add behavior back to a caller-specific path just because it is convenient.

## Module responsibilities

### `agent.Service`

`agent.Service` is the production seam for agent work. Callers should ask it to do one concrete thing:

- send a message to an existing web session
- run a non-private channel/group chat using a Stella-derived key
- run a scheduler job using a scheduler-derived session ID
- run or resume a delegate child session
- mint a task session under the resolved executor agent
- resolve the main private user session

`Service` is allowed to translate those intents into `session.Request` values. Edge callers should not set `CreateIfMissing`, `AllowExactIDCreate`, or `RequireKind` themselves.

Current important methods:

| Method                                                        | Purpose                                                           | ID trust model                                                          |
| ------------------------------------------------------------- | ----------------------------------------------------------------- | ----------------------------------------------------------------------- |
| `Chat`                                                        | Foreground chat for an existing or newly generated session        | caller-supplied `SessionID` is resume-only                              |
| `ChatForScheduler`                                            | Scheduler-initiated run                                           | exact-create allowed because scheduler derives `SessionID`              |
| `ResolvePrivateChannelSession` / `ResolveGroupChannelSession` | Resolve a private or group channel session without running a turn | trusted channel key, requires `KindChat`; group id owns a group session |
| `NewSession`                                                  | HTTP/Web UI create session                                        | generated ID only                                                       |
| `MintTaskSession`                                             | Task system creates a worker session                              | generated ID under resolved executor agent                              |
| `Delegate`                                                    | Run/resume delegate child session                                 | caller/model supplied `SessionID` is resume-only                        |
| `ResolveMainSession`                                          | Resolve or create private main session                            | generated or promoted main session                                      |

### `session.Registry`

`session.Registry` owns session lifecycle and policy:

- create and resume session records
- validate user and agent ownership
- validate kind on resume
- reject archived sessions
- resolve main sessions
- list sessions and review candidates
- convert a validated `session.Info` to `memory.Session` via `MemoryScope`

`session.Request` is low-level plumbing. It is intentionally flexible because `Service` and tests need to express different policies. Production edge callers should not construct it directly.

### `runtime.Runtime`

`runtime.Runtime` executes already-validated sessions. It does not create sessions, repair missing metadata, or decide whether a caller is allowed to use a session.

Runtime owns per-turn execution:

1. get or create the runner for the validated session
2. add user, agent, project, session, and channel values to context
3. run pre-agent hooks
4. compact if needed
5. update session last-active/title metadata
6. assemble history from memory
7. build the effective system prompt, including session snapshot handling
8. run before-run hooks
9. apply system override and excluded tools
10. append the user message
11. stream runner events
12. persist assistant/tool output
13. handle timeout notices and errors
14. run post-agent hooks

Runtime also enforces one active turn per session. A second concurrent chat for the same session returns `ErrSessionBusy` instead of interleaving transcript writes.

### `memory.Provider`

Memory stores and assembles content. It does not own session authorization or lifecycle policy.

Memory is responsible for:

- bootstrapping conversation storage
- appending messages
- assembling history within a token budget
- compaction and summaries
- profile, soul, constraints, knowledge, and changelog data
- session snapshots
- session metadata storage through `SessionManager`

A provider may store session metadata, but `session.Registry` owns the policy for using that metadata.

## Session and memory boundary

The most important boundary is this:

**Session decides whether a conversation container may be used. Memory decides what content is inside that container.**

Session owns:

- `UserID`
- `AgentID`
- `ProjectID`
- `Kind`
- `Channel`
- archived state
- title and last-active metadata
- exact-ID creation policy
- resume/kind validation
- main session resolution
- review candidate policy

Memory owns:

- messages
- context assembly
- summaries and compaction
- profile/soul/constraints/knowledge
- snapshots and changelog

Hard rule:

```go
// Production code derives memory scope only from a validated session.Info.
// MemoryScope validates the session invariant and fails closed on a bad Info.
scope, err := svc.Sessions.MemoryScope(validatedInfo)
```

`session.Info` is a distinct, validated session-domain type — not an alias of `memory.SessionInfo`. All production code, including `runtime.Runtime`, obtains `memory.Session` only through `MemoryScope`; there is no runtime direct-construction exception. A group session carries a durable, validated `GroupID` (persisted on `ctx_conversation.group_id`, with `UserID == GroupID`). For a private session, read, write, and compaction resolve the same partition. A group session shares one durable canonical scope for read and write; group compaction is unsupported — `CompactSession` rejects it with `ErrGroupCompactionUnsupported` because group history is assembled from the group event log, not the LCM conversation. Low-level memory tests may still build a `memory.Session` directly.

## Session kinds and channels

Session kind describes why the session exists. Channel describes where it originated.

| Kind        | Owner                          | Visible to users?  | Creation path                           |
| ----------- | ------------------------------ | ------------------ | --------------------------------------- |
| `main`      | private user chat              | yes                | `ResolveMainSession`                    |
| `chat`      | normal foreground/channel chat | yes                | channel resolvers, `Chat`, `NewSession` |
| `delegate`  | child agent work               | usually hidden     | `Delegate`                              |
| `task`      | async task worker run          | hidden by default  | `MintTaskSession`                       |
| `scheduler` | scheduled job run              | hidden or filtered | `ChatForScheduler`                      |

Typed resume must validate kind. A scheduler run must not resume a delegate session even if the ID matches. A channel session must require `KindChat` even though channel keys are trusted.

Human messages are accepted on every kind. The web UI may send a message to a `delegate`, `task`, or `scheduler` session just like a `chat` session. Human ingress contends for the runtime guard directly. Agent-originated `session.create` and `session.send` inputs are first recorded in the durable Session inbox, then use a bounded process-local FIFO for fairness before entering the same runtime admission guard. At the normal input-append point, LCM claims the inbox row and writes the transcript message in one transaction. The guard remains the one-active-turn correctness boundary.

## ID trust model

Not all session IDs are equal.

### Untrusted IDs: resume-only

These IDs come from users, HTTP paths, model tool calls, or any place the agent can influence.

- `POST /sessions/{sessionId}/messages`
- session tool `session_id`
- regular `Service.Chat` request `SessionID`

For these paths, a non-empty ID means: **load an existing session and validate kind/ownership**. If it is missing, return not found. Do not create a new row with that exact ID.

### Trusted IDs: exact-create allowed behind Service methods

These IDs are derived by Stella systems, not by users or models.

- channel/group session keys
- scheduler run session IDs

Only dedicated Service methods should exact-create trusted IDs. They must also set `RequireKind` so a collision with another kind fails closed.

## Runtime turn preparation

Runtime builds the effective prompt every turn, not only when the runner starts.

### Snapshot prompt

Session snapshots prevent background memory updates from changing an active conversation unexpectedly.

Runtime snapshot flow:

1. If a per-run system override is present, use it as the base system and skip snapshot reconstruction.
2. Otherwise, if memory implements `SessionSnapshotStore`, call `GetOrCreateSessionSnapshot` for `(session_id, user_id, agent_id)` on every turn.
3. Pass `SnapshotVersion` into the prompt builder.
4. Run before-run hooks with that base system prompt.
5. Apply the resulting system prompt to the runner via per-run context override.

Why skip snapshots when `systemOverride` is set: delegate turns pass an explicit system prompt assembled by the parent runner plus preset additions. Rebuilding the snapshot prompt again would duplicate or conflict with that explicit override.

### Timeout semantics

A chat timeout is a resumable stop, not a hard failure. Runtime persists and streams a friendly continuation notice and does not forward `ErrChatTimeout` to callers. Non-timeout errors still stream as `Event{Err: ...}`.

This matters for delegate and scheduler callers because they treat any stream error as a failed run.

### Concurrency

Runtime allows at most one active `AgentRun` per Session. PostgreSQL owns that invariant across server processes and binds each Run to one process-boot identity; the process-local active-turn gate and Agent-send FIFO are only fast rejection and fairness boundaries. Run-owned transcript, memory, and Session-activity writes validate the Run owner in the transaction that commits the write. Abort, completion, and lease expiry are linear durable transitions, and an expired Run is interrupted rather than transferred or replayed.

Agent-originated Session sends persist an inbox receipt before admission. Successful admission atomically links that receipt to its `AgentRun`; failed pre-admission work remains unlinked and terminal. Startup recovery may append legacy unlinked input, but linked work follows its terminal Run and never starts a model or tool turn during recovery.

Each Session also has a monotonic Sandbox compute generation. Losing compute fences the old generation and proves its resource absent before creating a replacement; stale exec, process, and compute-filesystem operations fail closed. Workspace access remains independent of Run and compute-generation ownership.

### Live event fan-out

Every admitted turn is owned by the server lifecycle, not by an HTTP connection. Runtime tees its events through a per-runtime `SessionHub` so a browser can navigate, refresh, or temporarily disconnect without stopping the agent:

- `Runtime.Chat` publishes each event to the hub in addition to the caller's channel. Losing the initiating message stream detaches that observer while the turn continues.
- Publishing never blocks the turn. The hub coalesces adjacent text/reasoning deltas and keeps up to 4,096 replay entries or 8 MiB of process-local replay for a newly attached observer; after that ceiling, reconnects receive future events and reconcile from persisted history when the turn ends.
- When the turn ends, the hub closes its subscriber channels. `POST /api/agents/{agentId}/sessions/{sessionId}/stop` is the separate, explicit cancellation path.

`GET /api/agents/{agentId}/sessions/{sessionId}/events` subscribes a read-only SSE stream that reuses the same AI-SDK UI message encoding as the message-send endpoint. The initiating Run's primary stream and same-process attaches remain local. If another process owns the durable live Run, the endpoint returns structured `503`, `Retry-After`, and `run_id`; the client polls durable Run/transcript state instead of relaying tokens between processes. It returns `204` only when PostgreSQL proves that no Run is active. The Web UI reloads persisted history after the stream settles.

## Caller flows

### Web/API message

```text
HTTP POST /api/agents/{agentId}/sessions/{sessionId}/messages
    -> Server validates auth and agent access
    -> Service.Chat(SessionID=sessionId, ...)
    -> Registry loads existing session, validates ownership and kind
    -> Runtime.Chat(validatedInfo, message)
```

The HTTP path `sessionId` is untrusted and resume-only. Unknown IDs must not create sessions.

### Web/API create session

```text
HTTP POST /api/agents/{agentId}/sessions { kind: main|chat }
    -> Service.ResolveMainSession or Service.NewSession
    -> Registry creates/promotes/generates the session
```

The public create API should not create internal `scheduler`, `task`, or `delegate` sessions.

### Private channel direct message

```text
channel resolves user + agent
    -> Service.ResolveMainSession
    -> Service.Chat(existing main session)
```

Private user channels converge on the main session.

### Group or shared channel message

```text
channel derives SessionKey and resolves the canonical GroupID
    -> Service.ResolveGroupChannelSession(SessionKey, GroupID, AgentID, Channel)
    -> Registry exact-creates only because the key is Stella-derived
    -> Service.Chat(validated group session)
    -> Runtime.Chat
```

The channel key is trusted, but resume still requires `KindChat`.

### Scheduler job

```text
scheduler derives run session ID
    -> Service.ChatForScheduler(SessionID, KindScheduler, ChannelScheduler)
    -> Registry exact-creates only because scheduler owns the ID derivation
    -> Runtime.Chat
```

Do not fall back to an arbitrary default service when a job has an explicit `AgentID`; that hides routing bugs.

### Session managed-run adapter

```text
parent runner executes session.create/send
    -> session tool passes message + optional preset/session_id
    -> internal DelegateTool resolves preset and run options
    -> Service.RunDelegateSession
    -> Service.Delegate
    -> Registry creates generated delegate session or resumes existing delegate session
    -> per-Session FIFO fairness
    -> Runtime.ChatAdmitted
```

Rules:

- the model-facing registry exposes `session`, not `delegate`
- supplied legacy delegate `session_id` is resume-only
- `session.create` creates a generated delegate session through the internal adapter
- child sessions inherit user, agent, and project scope
- call depth and Session ancestry travel in context; the runtime rejects depth overflow and cycles
- sibling and nested calls share a 16-call atomic budget allocated by the root runtime turn
- the root deadline and cancellation cover queue hold and every nested turn
- runtime options union ancestor excluded tools, while nested runs drop inherited channel chat binding
- agent input persists actor ID and source Session ID, and provider rendering marks it information-only

### Task worker session

```text
task creation resolves owner agent and optional project
    -> Service.MintTaskSession(userID, agentID, projectID)
    -> task row stores session_id
    -> run row records the task session_id and executor_agent_id
    -> worker runner uses the resolved executor agent scope
```

The task's worker session is created under the task owner/manager agent and optional project. A later run may still use a run-level executor override via dispatch hints.

### Reflect review

Reflect should use registry review listing and `Registry.MemoryScope` so review candidates obey session policy and internal kinds such as delegate/task/scheduler can be excluded.

## Testing rules

Session architecture tests should target the seam that owns the rule.

| Rule                                                        | Test at                                   |
| ----------------------------------------------------------- | ----------------------------------------- |
| User/model supplied IDs are resume-only                     | `agent.Service`                           |
| Trusted channel/scheduler IDs exact-create and require kind | `agent.Service`                           |
| Ownership/kind/archive validation                           | `session.Registry`                        |
| Turn assembly, snapshot prompt, timeout, busy guard         | `runtime.Runtime`                         |
| Task session owner is executor                              | `internal/tasks` dispatcher/minter tests  |
| HTTP create/message contract                                | `internal/server` and generated API types |

Useful guards:

```bash
# Edge callers should not bypass Service by using Registry.Ensure directly.
rg "\.Sessions\.Ensure" internal cmd --glob '!**/*_test.go'

# Policy switches should not appear outside session plumbing and Service intent methods.
rg "AllowExactIDCreate|CreateIfMissing|RequireKind" internal cmd \
  --glob '!**/*_test.go' \
  --glob '!**/session/**'

# Production code should not hand-build memory.Session except through approved seams.
rg "memory\.Session\{" internal cmd --glob '!**/*_test.go'
```

The grep checks are not a substitute for tests. They are tripwires for architectural drift.

## Adding a new agent entry point

When adding a new way to run an agent:

1. Identify the business intent in plain language.
2. Decide whether the session ID is untrusted resume-only or trusted system-derived.
3. Add or reuse an `agent.Service` method for that intent.
4. Make `Service` call `session.Registry` with the correct create/resume/kind policy.
5. Pass only validated `session.Info` to `runtime.Runtime`.
6. Ensure memory operations use `Registry.MemoryScope` or runtime-validated scope.
7. Add tests at the seam that owns the rule.
8. If HTTP changes are involved, update OpenAPI first and run `mise run generate:api`.

## Anti-patterns

Avoid these patterns:

```go
// Edge caller composing lifecycle policy directly.
svc.Sessions.Ensure(ctx, session.Request{AllowExactIDCreate: true, ...})
```

```go
// User/model supplied ID creates a session.
CreateIfMissing: true,
AllowExactIDCreate: true,
ID: req.SessionID,
```

```go
// Runtime called with a session.Info that did not come from Registry.
rt.Chat(ctx, session.Info{ID: id}, msg)
```

```go
// Production code hand-builds memory scope from request fields.
mem.Append(ctx, memory.Session{ID: sessionID, UserID: userID, AgentID: agentID}, msg)
```

## Verification before merging architecture changes

For changes touching this architecture, run the project-required checks:

```bash
mise run format
mise run build
mise run test
```

For HTTP API changes, also run:

```bash
mise run generate:api
```

For database schema changes, follow the goose migration workflow in `rules/schema-design` before editing any table.
