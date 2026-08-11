# Plan: Implement Sandbox Architecture v2

- **Status:** APPROVED — implementation tracked in CherryHQ/stella#828; Docker Compose remains the hard gate before Kubernetes
- **Issue:** CherryHQ/stella#828 (implementation tracking)
- **Design PR:** CherryHQ/stella#829 (merged)
- **Current-main reconciliation:** 2026-08-10, after PRs #940 and #962
- **Architecture source:** `docs/design/2026-08-01-sandbox-architecture-v2.md` rev 8, D1–D60
- **Current plan:** `docs/design/2026-08-02-sandbox-architecture-v2-implementation-plan.md`
- **Historical authoring path:** `~/.agents/sessions/stella/2026-08-02-sandbox-architecture-v2-implementation/plan.md` (non-authoritative after repository reconciliation)
- **Execution:** one logical phase at a time; every phase ends with its Acceptance block, review, commit(s), and a handoff before the next phase starts

## Problem

Stella currently works because `stellad`, its sandbox, and durable files usually share one machine and one process lifetime. That accidental shape leaks into public contracts and correctness mechanisms:

- `pkg/sandbox.Session` exposes host paths through `ResolvePath` and `ResolveWritePath`; tools and Workspace APIs perform `os.*` outside the sandbox contract.
- `agent.workspace`, `project.base_dir`, `HostPath`, mutable assets, group homes, and user-less Agent directories mix logical data identity with `STELLA_HOME` coordinates.
- active-turn exclusion, channel serialization, Publisher lookup, Docker resource identity, OAuth flows, and login rate limits depend on process memory.
- Session activity/viewed watermarks are now durable, while `running` remains process-local; agent-originated `session.send` now has a durable `ctx_session_inbox`, transcript-only startup recovery, and a process-local `turnqueue` that cannot serialize across replicas.
- the Helm deployment is intentionally single-replica, while the target must safely support multiple homogeneous `stellad` replicas and ordinary Kubernetes Session Pods.
- a failed executor can leave outcome-unknown work, stale containers, stuck running rows, or accepted asynchronous input. Retrying by guess would be worse than surfacing interruption.
- Skill current state is split between release embeds, PG rows, extracted host directories and project files; without an authority cutover, multi-replica execution would preserve a PG/disk mirror and implement a disposable DB Skill snapshot mechanism only to delete it later.

The implementation must reach the architecture in rev 8 without creating a second scheduler, a long-lived Session owner, a file service, a new broker, a Skill sync engine, or a second durable state machine for the same work.

## Success criteria

The program is complete when all of the following are observable:

1. User and group persistent data resolve through typed `PrincipalHome`; Agent-private data resolves through `AgentHome`; no public contract exposes a host path.
2. A Session owns one reconnectable `SessionSandbox` generation, while any capable replica can execute a later `AgentRun` against it.
3. `AgentRun` is the only running execution lease across Chat, agent-originated Session send, channel, Webhook, Scheduler, Goal, and Delegate; expiry, abort, stale writes, and generation fencing fail closed. Session activity/viewed metadata remains presentation state, not an execution lease.
4. Accepted asynchronous input is durable, ordered by ChatBinding, deduplicated only by stable source identity, and never silently skipped or replayed after an unknown outcome.
5. A three-replica Docker Compose deployment passes the cross-replica protocol and crash-recovery gate before Kubernetes work begins.
6. Kubernetes passes its own POSIX, PVC topology, Pod fencing, security, and node-failure conformance before Helm permits more than one replica.
7. Current supported local, `none`, and single-daemon Docker behavior remains available throughout the migration; deliberate removals (`/agent`, `/model`, mutable asset mirroring) are documented and tested.
8. Builtin Skills execute from one digest-pinned release bundle; every mutable Skill has one Home filesystem authority, and the PG→Home cutover cannot leave dual writers/readers or silently lose legacy/Reflect state.

## Explicit non-goals

- persistent VMs or guest rootfs;
- an Executor Fleet, Session actor, Stella scheduler, or owner-directed replica RPC;
- smolvm, Kata, OpenSandbox, multiple Docker daemons, or a generic remote Provider;
- cross-replica token-level event relay, Redis/NATS, or sticky routing as a correctness requirement;
- transparent replay of outcome-unknown commands, writes, publishes, or interrupted Runs;
- online Home dual-write migration, automatic storage expansion/sharding, or a backup/restore product;
- mixed-version rolling execution or zero-downtime schema contracts in the initial implementation.
- PG/Home Skill dual-write, a Skill watcher/sync service, or a PG current-state index after filesystem cutover;
- preserving PG `skill_changelog` or cross-file database transaction semantics after arbitrary POSIX Skill writes become authoritative.

## How we got here

The plan is grounded in:

- the complete architecture decision record, `docs/design/2026-08-01-sandbox-architecture-v2.md` rev 8;
- the secrets compatibility design and multi-replica readiness audit beside it;
- current sandbox contracts and backends in `pkg/sandbox/`, `plugins/sandbox/`, and `internal/agent/sandbox/`;
- current runtime ownership and event paths in `internal/agent/runtime/`, `internal/agent/service.go`, and `internal/server/sessions.go`;
- current channel queue, group route/dispatch, Publisher, and startup wiring in `internal/channel/` and `cmd/stellad/channel_runtime.go`;
- current Home layout in `internal/agent/workspace.go`, project path derivation, mutable asset/blob/share consumers, and Workspace access;
- Goal, Scheduler, Delegate, Webhook, OAuth flow, rate-limit, config snapshot, Helm, and Docker Compose implementations;
- repository rules for schema, goose, sqlc, OpenAPI-first APIs, Go safety patterns, Web UI, system tests, and docs.

An independent Fable architecture review found no direction blocker in rev 5. Rev 6 moved the shared-control and Docker Compose multi-replica gate before Kubernetes. Rev 7 added the fail-closed migration for object-only mutable assets. Rev 8 replaces the planned PG Skill scratch authority with immutable builtin bundle plus mutable Home filesystem authority. Fable found and closed six concrete Skill migration defects before approving that revision in round 2.

## Design decisions

### 1. Persistent identity belongs to a typed Home registry

`internal/home` will be the deep module that owns logical Home identity, Store selection, lifecycle, legacy registration, and attachment resolution. Phase 1 stops at permanent tombstone and preserves physical bytes; provider-fenced purge and Store migration land only after the Filesystem boundary. Callers receive opaque attachments; they do not derive paths or PVC names.

```text
PrincipalHome = (principal_kind: user|group, principal_id)
AgentHome     = (principal_kind, principal_id, agent_id)
```

`pkg/sandbox` will contain only the stable `HomeAttachment` value consumed by compute Providers. It will not expose `HomeStore`, registry queries, or physical root paths.

Alternatives rejected:

- **Continue treating groups as `group-<id>` users:** string prefixes are not a type boundary and cannot safely drive cross-Provider placement or deletion.
- **One PVC per user/group:** unnecessary object count and cost; the initial PrincipalHome Store is one explicitly configured RWX Pool with opaque subpaths.
- **Put Home placement inside SandboxProvider:** compute changes must not silently relocate durable data.
- **Use `agent.workspace` as identity:** one Agent definition cannot represent per-user and per-group AgentHome data.

Tradeoff: `storage_home` keeps typed owner IDs as immutable audit metadata rather than cascading away with owner rows. Service-layer creation validates the current owner; deletion tombstones the Home before the owner disappears.

### 2. Filesystem is a deep boundary, not a remote file service

The public sandbox contract will stop returning host paths. `internal/fsops` will implement validated file operations once; local/`none` use it directly, while Docker/Kubernetes invoke the same behavior through one-shot provider-native exec.

To preserve Stella's single-binary rule, `/opt/stella/stella-fs` and `/opt/stella/stella-exec` are installed as restricted multicall entry points to the digest-pinned `stellad` binary, not separate services or independently versioned binaries. They accept bounded stdin, emit a narrow framed result, never listen on a socket, and receive only the environment required for that invocation.

Alternatives rejected:

- **Persistent filesystem sidecar/service:** adds another identity, lifecycle, authentication surface, and availability dependency.
- **Direct S3 object operations:** cannot preserve arbitrary CLI/POSIX behavior or concurrent filesystem semantics.
- **Keep local host-path fast paths in callers:** guarantees future Providers regress to the same coupling.

Tradeoff: Workspace access may wake an idle Session Pod. That cost is accepted because every current Workspace URL already identifies an exact Session and does not justify a second file-access Pod lifecycle.

The mutable asset authority cutover is an explicit offline migration, not a delete-and-hope upgrade. Phase 1 records migration state; Phase 2 adds `stellad storage migrate-assets --dry-run|--json`, materializes every object-only mutable asset into the typed PrincipalHome, verifies count/size/digest, records completion by CAS, and leaves remote objects untouched. A server configured with the legacy authority refuses startup until the marker is complete. Upgrade instructions require retaining the old `STELLA_BLOB_S3_*` configuration and stopping old writers while the command runs.

> Make `$STELLA_ASSETS_DIR` ordinary PrincipalHome data and remove mutable object mirroring/hydration.

**Review (Fable):** The initial plan deleted an authority that documented deployments may use as the only durable copy; object-only assets would become unreachable after upgrade.

**Resolved:** Added D55 and the offline, fail-closed, idempotent migration command/marker before mirror or hydrate can be removed.

### 3. Skill authority is release bundle plus Home filesystem

Release-owned builtin Skills come from one deterministic `resources.Registry` manifest and execute from the same digest-pinned bundle in local/Docker/Kubernetes. Every mutable scope becomes an ordinary filesystem namespace: shared RWX SystemSkillRoot/SystemAgentSkillRoot, UserHome, AgentHome, or ProjectRoot. Scope+name is the rename-free logical identity.

Phase 0 is a separate stacked prerequisite: build the bundle, stop scanning extracted builtins, classify legacy custom system roots fail-closed, and reuse `agent.enabled_builtin_skills` as a versioned per-Agent policy without adding a table. Ordinary Agent updates must stop clobbering that column; all legacy arrays retain current all-enabled behavior.

Phase 2 performs the only mutable authority cutover after typed Homes and `stella-fs` exist but before AgentRun/multi-replica. Managed writes use immutable revision directories plus contained symlink flip; ordinary CLI edits retain POSIX semantics. The offline migration exhaustively disposes active/deprecated/manual/Reflect rows, archives changelog, migrates Reflect usage to logical identity+digest, and records a marker only after complete verification. Marker before/after states each have one authority; there is no dual-write or restore-on-miss.

Alternatives rejected:

- **Keep PG Skill content and materialize Session scratch:** preserves two content shapes and builds a mechanism Homes make unnecessary.
- **Move mutable Skills before HomeStore/stella-fs:** writes new host-coupled paths that the next phase must delete.
- **Wait until Kubernetes:** forces AgentRun/Compose to implement and test doomed PG snapshot semantics.
- **Add an activation table:** the existing Agent JSONB column is sufficient when guarded by one dedicated row transaction.
- **Rename a directory over an existing directory:** not a portable atomic replacement; managed updates need revision symlink flip.

Tradeoff: arbitrary POSIX edits cannot retain PG cross-file transaction/audit guarantees. Reflect uses tree digest conflict detection and derived usage telemetry; an uncoordinated writer racing the final flip follows ordinary POSIX winner semantics rather than a fake transaction promise.

> Make mutable Skill files authoritative in Homes, while keeping builtin Skills release-owned.

**Review (Fable, Skill authority round 1):** `CHANGES REQUIRED`. The proposal allowed broad Agent updates to erase policy, inferred unsupported legacy-array semantics, omitted Reflect migration, promised impossible directory replacement, hid legacy custom system Skills, and left PG-only metadata without a cutover disposition.

**Resolved:** Broad updates stop writing policy; all arrays preserve current behavior; Reflect moves to filesystem digest identity plus logical usage telemetry; managed writes use contained symlink flip; legacy roots fail closed at manifest skill-root granularity; metadata/status/changelog have exhaustive active/archive dispositions.

**Review (Fable, Skill authority round 2):** `DECISION: APPROVED`. No remaining mandatory finding; the two-authority model, phase order and acceptance gates are approved.

### 4. Session owns compute; AgentRun owns temporary execution

`pkg/sandbox` will evolve from a creator-only Session abstraction to a complete Provider lifecycle:

```text
Provision(intent/spec) -> SandboxRef
Open(ref)              -> Sandbox
Inspect(ref/intent)    -> state
Destroy(ref)           -> idempotent completion
```

PostgreSQL persists `SessionSandbox`, normalized spec, deterministic provisioning intent, immutable resource identity, generation, lifecycle, and keepalive. A lightweight reconciler runs on every replica and competes only through CAS; it is not an owner.

`internal/agent/run` will own AgentRun admission, heartbeat guard, abort, execution-start marker, guarded finalization, and terminal state. Every source domain references its Run but keeps only its own receipt/queue/budget/reply policy. Durable Session activity/viewed watermarks remain presentation metadata; after Phase 3, `running` is derived only from a valid AgentRun.

Alternatives rejected:

- **Long-lived Session/Agent owner:** creates sticky routing and a replica-to-replica RPC data plane.
- **Transfer a running Run after lease loss:** cannot prove whether the prior executor performed a model, tool, filesystem, or third-party side effect.
- **Keep Goal or group execution heartbeats beside AgentRun:** two leases for one execution can disagree and produce duplicate work.

Tradeoff: an accepted message can become visibly interrupted without an automatic response. The receipt and transcript remain durable; unknown work is not guessed safe to replay.

### 5. Durable ingress keeps source receipts and one generic ChatBinding FIFO

`ctx_chat_input` is the only new generic direct/channel delivery table. It is simultaneously the ingress receipt, lane item, barrier, batch member, and one-time Run relation. Existing `ctx_group_message` remains the immutable group receipt; `ctx_group_outbox` is narrowed into one expiring `GroupRoute` claim.

PR #962's existing `ctx_session_inbox` remains the source-domain receipt for agent-originated Session send. Phase 3 adds its AgentRun relation and keeps its exact source/target Session provenance plus transcript-only recovery. It does not join the ChatBinding FIFO, batch, background dispatch, or hold a running lease. Live send atomically persists/validates the receipt, admits one AgentRun, links the two, and projects the inbox input; successful admission then preserves synchronous result streaming. Busy/no-capacity ends the receipt with an explicit failure. A committed Run interrupted by process death is never replayed; startup recovery only idempotently appends legacy/unassociated pending receipts to the transcript without creating a Run.

The dispatcher uses transactional PostgreSQL notification only as a wakeup, takes a per-binding transaction advisory lock, reauthorizes at admission, and atomically creates a running Run only when the local replica has capacity.

`internal/agent/session/turnqueue` is deleted or retained only as optional local fairness before database admission. Two replicas must remain correct without it; the AgentRun partial unique index, not a local queue, is the Session serialization boundary.

Alternatives rejected:

- **River as the chat queue authority:** duplicates input state and makes ordered prefix batching span two queues.
- **One Run per message:** throws away bounded batching and amplifies model/setup cost.
- **Content-hash deduplication:** silently drops distinct legitimate messages with identical text.
- **A second physical-chat routing FIFO:** only needed by in-band `/agent`, which is removed.

Tradeoff: strict FIFO means a transiently failing head can block the lane. The implementation provides bounded backoff, blocked-lane observability, and audited administrator rejection rather than silently skipping it.

### 6. PostgreSQL is the coordination authority, not an event broker

Each replica owns exactly one pool-external serialized control session for small wakeups, health probes, and the global pull/WebSocket channel advisory lock. Transaction-pooling proxies are rejected.

Token deltas remain local. Durable Run state and transcript are the recovery authority; a remote live attach returns structured `503 + Retry-After + run_id`, and the Web UI polls the Run resource and transcript.

Alternatives rejected:

- **PostgreSQL NOTIFY for token streams:** payload and fan-out characteristics are wrong for large ephemeral events.
- **Redis/NATS initially:** a new service for non-authoritative presentation data is unjustified.
- **Route Runs back to the channel leader:** converts a narrow ingress lock into a control-plane owner.

Tradeoff: watching a Run started on another replica loses token-level immediacy until a real authenticated event relay becomes a product requirement.

### 7. Docker Compose is the distributed-protocol gate

Kubernetes must not be the first environment in which multi-replica correctness is exercised. Phase 4 runs three named `stellad` containers against:

- one external PostgreSQL service;
- one shared Docker daemon;
- one reconnectable named-volume Home Store;
- explicit per-replica HTTP ports, avoiding a test-only load balancer.

The harness targets replicas directly so it can prove cross-replica admission, Workspace access, abort, event fallback, leader failover, Publisher reconstruction, executor crash recovery, and fencing.

Kubernetes starts only after this gate passes. Its phase is limited to platform-specific Pod/PVC/CSI/topology/RBAC/network-policy behavior and reuses the already-proven distributed protocol.

Alternatives rejected:

- **Build Kubernetes first:** mixes Stella protocol defects with scheduler, CSI, and RBAC failures.
- **Use only unit tests before Kubernetes:** misses process death, independent caches, DB sessions, daemon reconnect, and real TCP behavior.

Tradeoff: Phase 4 adds a slower local/CI gate, but it sharply narrows failures in the more expensive Kubernetes phase.

### 8. Activation is staged and fail-closed

The final state never has duplicate authorities, but intermediate implementation PRs may introduce unused schema/types before cutover. Production remains single-replica through Phase 3.

- Phase 0 separately cuts builtin authority and Agent Skill policy, but leaves PG mutable Skill content untouched.
- Phase 1 introduces Home/shared Skill-root identity while old local/Docker mutable Skill behavior continues.
- Phase 2a cuts file consumers to Filesystem. Phase 2b freezes writers, migrates PG Skills/Reflect to Homes, records one marker, then deletes PG current-state readers/writers and public host-path APIs.
- Phase 3 cuts execution/input correctness to PostgreSQL, pins filesystem catalog/policy revisions, and still rejects multi-replica configuration.
- Phase 4 enables the Docker/Compose multi-replica flag only after shared policy invalidation, Reflect CAS, control-state checks and journeys pass.
- Phase 5 enables Helm `replicaCount > 1` only after Kubernetes and shared Skill-root conformance pass.

There is no “best effort” fallback to process-local state. Missing shared PostgreSQL, session-pooling semantics, a distributed-capable Provider, durable channel metadata, or DB-backed security state is a startup error.

### 9. API and UI changes are spec-first and ship with their consumers

Phase 2 removes host-path fields from Workspace responses and updates the generated TypeScript client and Web consumer in the same change. The existing `scope=user` wire value remains a compatibility alias meaning the shared PrincipalHome root; it is documented rather than renamed mid-program.

Phase 3 adds a read-only AgentRun resource and abort custom method under the Session hierarchy, plus structured error details for duplicate, busy/recovering, and remote-live states. Proposed paths:

```text
GET  /api/agents/{agentId}/sessions/{sessionId}/runs/{runId}
POST /api/agents/{agentId}/sessions/{sessionId}/runs/{runId}/abort
```

Every API change starts in `api/spec/domain/sessions/`, regenerates Go/TypeScript contracts, then updates server and Web code. No handler or wire type is hand-written around generated contracts.

The Phase 0 Agent Skill activation endpoint starts in `api/spec/domain/agents/`. It uses exact logical refs, authorizes through Agent `Manage`, changes only the reused policy column, and keeps content mutation under the existing Skill policy until the Phase 2 filesystem cutover.

### 10. Testing uses the lowest sufficient layer

- pure identity, transition, guard, path, batching, and policy logic: Go unit tests;
- PostgreSQL constraints, CAS, advisory locks, migrations, and sqlc queries: DB integration tests;
- real process startup, SSE, abort, restart, TCP, and asynchronous reconciliation: existing subprocess system tests;
- independent replicas plus shared PostgreSQL/daemon/storage and process kills: Phase 4 Compose suite;
- RWX/RWO semantics, scheduling, attach/detach, Pod UID fencing, RBAC, and network policy: Phase 5 Kubernetes conformance.
- Skill policy parsing/merge/digest logic: Go unit tests; symlink publication and PG→Home migration: filesystem/DB integration; cross-replica policy and Reflect invalidation: Phase 4 Compose.

Every phase runs the repository's mandatory `mise run format && mise run build && mise run test`. System, Compose, and Kubernetes gates are added only where their seams require them.

## What changes where

| Area                                                                               | Planned change                                                                                                                                                                                                                                                                         |
| ---------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/db/migrations/`                                                          | Hand-written goose migrations for typed Homes/shared Skill roots, Skill migration marker/Reflect usage identity, SessionSandbox, AgentRun, chat input/GroupRoute, OAuth flows, rate limits, and config revisions. One logical schema change per migration; every migration has `Down`. |
| `internal/db/queries/`                                                             | New focused sqlc files such as `storage_home.sql`, `ctx_session_sandbox.sql`, `ctx_agent_run.sql`, and `ctx_chat_input.sql`; evolve existing Session inbox queries; narrow group queries; guard transcript/source updates.                                                             |
| `pkg/db/sqlc/`                                                                     | Regenerated only through `mise run generate`; never hand-edited.                                                                                                                                                                                                                       |
| `internal/home/`                                                                   | New deep module for typed identity including shared Skill roots, Store configuration, attachment resolution, legacy registration, terminal tombstone, and later provider-fenced purge and local/Docker/Kubernetes adapters.                                                            |
| `pkg/sandbox/`                                                                     | `HomeAttachment`, Provider lifecycle, normalized spec/ref/state, error taxonomy, and removal of public host-path methods.                                                                                                                                                              |
| `plugins/sandbox/local`, `none`, `docker`                                          | Adapt to the Provider/Filesystem contracts; Docker gains deterministic identity, labels, capability probes, Open/Inspect/Destroy, and no `owner_pid` correctness.                                                                                                                      |
| `internal/fsops/` and `cmd/stellad/`                                               | Shared safe file operations plus restricted multicall `stella-fs`/`stella-exec` entry modes in the existing binary.                                                                                                                                                                    |
| `internal/agent/`                                                                  | Replace path-derived workspace identity; add AgentRun guard/finalization; move every top-level source including agent-originated Session send to one lease; demote/delete `turnqueue`; guard transcript and memory writes.                                                             |
| `internal/agent/sandbox/`                                                          | SessionSandbox lifecycle, BeginUse/keepalive, provisioning intent, reconciler, env refresh, file helper adapter, and generation checks.                                                                                                                                                |
| `internal/channel/`                                                                | `ctx_chat_input` ingress/dispatcher, barriers, batching, stable dedup, expiring GroupRoute, per-responder Web busy results, durable reply envelopes, and removal of process queue plus `/agent`/`/model`.                                                                              |
| `internal/controlplane/`, `cmd/stellad/channel_runtime.go`                         | One serialized PostgreSQL control session, leader acquisition/drain, cache invalidation, lifecycle startup order, and feature gates.                                                                                                                                                   |
| `internal/connections/oauth`, `internal/auth`, `internal/config`                   | PostgreSQL OAuth flows, shared rate limits, monotonic config revisions, and loss-tolerant invalidation.                                                                                                                                                                                |
| `api/spec/domain/sessions/`, generated API, `internal/server/`                     | Spec-first Workspace response cleanup, `client_message_id`, AgentRun Get/abort, structured 409/503 details, exact Session Workspace behavior, and read-only live attach semantics.                                                                                                     |
| `web/src/features/sessions/`                                                       | Stable client message IDs, AgentRun/transcript polling, terminal/interrupted presentation, remote-live fallback, and no stale `/events`-only loop.                                                                                                                                     |
| `internal/asset`, `internal/blob`, `internal/share`, session media                 | Remove mutable asset double authority while retaining immutable media/share snapshots.                                                                                                                                                                                                 |
| `resources/`, `internal/skills/`, Skill APIs/UI                                    | Deterministic builtin bundle, manifest-root legacy gate, versioned Agent policy, filesystem catalog/publication, PG→Home migration, Reflect digest/usage cutover, and removal of PG Skill current-state authority.                                                                     |
| `docker-compose.yml`, `test/compose/`, `mise.toml`                                 | Preserve the one-replica developer example; add a dedicated three-replica conformance topology and `mise run compose-test`.                                                                                                                                                            |
| `plugins/sandbox/kubernetes/`, `deploy/helm/stella/`, `test/kubernetes/`           | Kubernetes Provider/HomeStore, Session Pod specs, PVCs/affinity, trusted provisioner, RBAC/security/network policy, chart gates, and conformance harness.                                                                                                                              |
| `README.md`, `web/content/docs/`, `resources/skills/system/stella/`, system prompt | Update supported behavior, configuration, commands, topology, failure semantics, and EN/ZH docs as each behavior lands.                                                                                                                                                                |

## Migration and PR order

The implementation baseline is **16 Draft PRs**. Every PR body says `Refs #828`; no child Issue is created, no PR says `Closes #828`, and every PR stays Draft until V explicitly changes its state. Drafts open just in time rather than all at once.

| ID  | Branch                          | Scope                                                                                                                                                                                       | Depends on |
| --- | ------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------- |
| 0.1 | `refactor/system-skill-bundle`  | Complete standalone Phase 0 in four commits: manifest/Registry, Provider projection/legacy gate, Agent policy API/UI, docs/gate                                                             | PR #829    |
| 1.1 | `storage/typed-homes`           | Phase 1 vertical slice: typed identity, registration, ready-root inspection, consumer routing, and terminal tombstone/fence; physical bytes remain                                          | 0.1        |
| 2.1 | `sandbox/filesystem-boundary`   | `fsops`, restricted helpers, local/none/Docker adapters, Filesystem conformance and outcome-unknown semantics                                                                               | 1.1        |
| 2.2 | `storage/home-physical-purge`   | Dedicated Draft after the provider/filesystem boundary: provider-fenced asynchronous physical destruction, terminal audit/retry, and offline Store migration                                | 2.1        |
| 2.3 | `storage/home-consumers-assets` | Non-Skill read/write/edit and Workspace API/UI migration, host parser boundary, mutable-asset migration/marker/Home cutover                                                                 | 2.2        |
| 2.4 | `skills/home-authority-cutover` | Filesystem Skill metadata/catalog/publication/admin path, observability, then offline PG Skill/Reflect/usage/archive migration after the asset marker exists                                | 2.3        |
| 2.5 | `sandbox/host-path-cleanup`     | Remove remaining public host paths, PG Skill current-state readers/writers and obsolete retries; run complete Phase 2 acceptance                                                            | 2.4        |
| 3.1 | `runtime/session-sandbox`       | Durable SessionSandbox/AgentRun/input schema, including `ctx_session_inbox` Run relation, deterministic Provider lifecycle, provisioning intents and exact-generation Workspace foundation  | 2.5        |
| 3.2 | `runtime/agent-run`             | AgentRun lease/guards/abort, lifecycle reconciler, source migration, Workspace linearization, catalog/policy pin and revision GC                                                            | 3.1        |
| 3.3 | `runtime/durable-input`         | ChatBinding FIFO/media/quota/batching, agent-send inbox admission/recovery, receipt backfill, GroupRoute, Session API/Web behavior, command/process-local cleanup and complete Phase 3 gate | 3.2        |
| 4.1 | `control/shared-state`          | Pool-external control session, OAuth/rate-limit/config/policy durability and managed-writer serialization                                                                                   | 3.3        |
| 4.2 | `channel/distributed-runtime`   | Global channel ingress leadership, cursor/ack/capability durability and reconstructable Publishers                                                                                          | 4.1        |
| 4.3 | `sandbox/compose-gate`          | Three-replica Compose harness/journeys, documentation and fail-closed Docker multi-replica activation                                                                                       | 4.2        |
| 5.1 | `sandbox/kubernetes-provider`   | Kubernetes dependency/frozen placement contract, Provider Pod/exec/watch/recovery and Session Pod compute                                                                                   | 4.3        |
| 5.2 | `storage/kubernetes-topology`   | RWX Pool/shared roots, RWO AgentHome PVC/affinity, trusted provisioner, Helm storage/security/RBAC/network/quota integration                                                                | 5.1        |
| 5.3 | `sandbox/kubernetes-gate`       | Kubernetes conformance, Phase 4 journey reuse, docs and fail-closed Helm multi-replica activation                                                                                           | 5.2        |

Count: `1 + 1 + 5 + 3 + 3 + 3 = 16`.

```text
#829 → 0.1 → 1.1 → 2.1 → 2.2 → 2.3 → 2.4 → 2.5
                              │      │
                              │      └─ Skill preparation may start after 2.1,
                              │         but its cutover/review base remains 2.3.
                              └─ Physical purge waits for provider-fenced filesystem operations;
                                 Workspace and asset work follows that boundary.

2.5 → 3.1 → 3.2 → 3.3 → 4.1 → 4.2 → 4.3 → 5.1 → 5.2 → 5.3
                              │                    │
                              │                    └─ storage/chart preparation may run beside
                              │                       compute after 5.1 freezes placement types.
                              └─ auth-state and channel work may be developed in parallel
                                 after the control-session contract commit, but merge in order.
```

PR count and worker count are independent. Separate COW clones may develop disjoint commits in parallel, then integrate them into the named Draft PR. Maximum substantive coding concurrency is two lanes; planning, fixtures and documentation may run ahead only against committed interfaces. A higher phase cannot claim Acceptance, merge, or activate before its predecessor gate passes.

Use `gh stack` for linear chains. Parallel preparation does not justify sibling PRs unless it gains an independent rollback/activation boundary; fake linear micro-PRs and one permanent 16-layer stack are both rejected. After #829 merges, retarget 0.1 to `main`. Each behavior/API/config PR carries its own README/docs/EN+ZH/builtin-skill/system-prompt delta where applicable; gate PRs aggregate phase acceptance and handoff rather than receiving all documentation debt.

Schema additions are expand-first. Old columns/tables are removed only in the PR that removes their last reader/writer. Asset authority switches before Skill authority; no phase leaves two active authorities under a multi-replica-enabled configuration. Split beyond 16 only when implementation evidence reveals a genuinely independent rollback boundary or one PR cannot have a single runnable Acceptance story; update this plan first rather than drifting silently.

### PMO execution and progress protocol

The main Sol agent is the program owner, not a feature coder. It owns dependency order, delegate briefs, COW isolation, acceptance evidence, adversarial review, commits, Draft PR metadata, plan/handoff synchronization, and the live Issue #828 dashboard. Substantive implementation is delegated one named PR or safe sub-slice at a time to Daily agents; mechanical generation/document synchronization may go to Fast agents.

- Every delegate works only in `~/.agents/worktrees/stella/<pr-id>-<slug>`, created as an APFS COW clone from the exact committed dependency head. No implementation runs in the source checkout.
- The clone removes inherited `.git/worktrees` registrations, configures `rerere`/`remote.pushDefault`, trusts mise locally, and retains no correctness dependency on another clone's uncommitted files.
- Delegates do not edit Issue #828, PR state, canonical plans, or shared tracking. They return changed paths, focused verification, unresolved risks, and handoff evidence; the main agent independently inspects and runs the required gates before committing.
- Only the main agent pushes, opens/edits Draft PRs, rebases stacks, records Acceptance, and changes the checklist. It never marks a PR Ready or merges without V's explicit instruction.
- Issue #828 is the live program dashboard. Update it immediately when work is delegated, a first commit or Draft PR appears, a check fails/passes, a dependency blocks/unblocks, a review changes scope, or a PR reaches its phase gate. Also reconcile it at every session handoff; “will update later” is not a state.
- Checklist states are `queued`, `in progress`, `verification`, `blocked`, and `done`. A checked box means the PR's complete planned Acceptance passed and its handoff was recorded, not merely that code exists.
- Each dashboard row records PR ID/scope, current state, branch/PR link, dependency, latest verified commit, last update time, next action, and blocker if any. The canonical plan remains the detailed contract; Issue #828 is the timely status projection.

## Tasks

### Phase 0 prerequisite: Builtin bundle and Agent Skill policy

**Why before Phase 1:** release-owned builtin authority and logical activation refs do not need Homes; landing them first prevents Home migration from carrying the startup-extracted builtin mirror forward. The current repository plan is `docs/design/2026-08-02-system-skill-bundle-phase-0-plan.md`; `~/.agents/sessions/stella/2026-08-02-system-skill-bundle/plan.md` is its historical authoring path.

- [ ] Generate and embed the deterministic mode-preserving builtin manifest; make `resources.Registry` the catalog/content authority and install one digest-pinned execution bundle for local/none/Docker. Isolating Providers expose its exact revision read-only at `/opt/stella/skills/builtin`.
- [ ] Before disabling the legacy scan/mount, classify `$STELLA_HOME/.agents/skills` at the same nested skill-root granularity as the current extractor. Any non-manifest root or unrecognized residue fails the capability gate with a complete operator inventory.
- [ ] Preserve `system/system_agent`; expose builtin as distinct read-only identity; keep existing precedence and no-lower-fallback behavior.
- [ ] Reuse `agent.enabled_builtin_skills` as typed versioned `AgentSkillPolicy`; all legacy arrays mean no disabled refs, non-empty arrays produce diagnostics, and broad Agent create/update paths never overwrite canonical policy.
- [ ] Add the spec-first exact-ref Agent activation API/UI and current-process runner invalidation; admin and durable creator share Agent `Manage`, while Skill content permissions stay unchanged.
- [ ] Do not migrate mutable PG Skill content, invent Home roots, add an activation table, or add a temporary sync/cache protocol.

**Acceptance:**

- [ ] Builtin manifest generation is deterministic; executable fixtures preserve mode; tampered/partial bundles and binary/image revision mismatch fail readiness.
- [ ] Direct builtin catalog/file reads do not scan an extracted directory or query SkillStore; DB name resolution still preserves system shadowing.
- [ ] A nested custom legacy system Skill blocks activation with its exact root listed; no custom root silently disappears.
- [ ] Changing every ordinary Agent field preserves policy bytes; empty/non-empty legacy arrays both preserve current all-enabled behavior until an explicit policy write.
- [ ] Admin/creator/other/delegated authorization, disabled-winner no-fallback, prompt/search/load/file/helper filtering, and next-turn invalidation tests pass.
- [ ] `mise run format && mise run build && mise run test` exits 0, plus Docker bundle contract checks on a supported trusted runner.

### Phase 1: Typed Home registry and local compatibility

**Why first:** every later Sandbox ref, mount, file operation, and Kubernetes volume needs a stable data identity that is not a machine path.

- [x] Add goose migrations for `storage_home`, `storage_migration`, and terminal lifecycle metadata.
  - Encode `home_kind=principal|agent|system_skill|system_agent_skill`, `principal_kind=user|group|nullable`, nullable owner/agent fields, lifecycle checks, singleton/per-Agent shared-root indexes, UTC timestamps, and permanent tombstone audit.
  - Do not cascade owner deletion into Home rows; destructive owner deletion must tombstone Homes first.
- [x] Add focused sqlc queries and transaction helpers for Ensure, Resolve, terminal tombstone, legacy registration, and monotonic migration observation.
- [x] Implement `internal/home` with a narrow deep API: typed keys, Store registry, opaque locator, `Ensure`, ready-root inspection, `Resolve`, and `Tombstone`.
- [x] Add one explicit immutable LocalStore identity for local/Docker compatibility layouts. Startup fails closed if persisted Homes or registration metadata reference another Store; changing it requires a future offline migration.
- [x] Add opaque SystemSkillRoot and SystemAgentSkillRoot identities in the existing shared RWX Store. They are narrow Skill namespaces, not a global Agent workspace; Agent delete is their only destructive lifecycle trigger.
- [x] Implement idempotent legacy registration derived from authoritative DB user/group/Agent rows:
  - `users/{userID}` → UserHome;
  - `users/group-{groupID}` → GroupHome;
  - each `agents/{agentID}` subtree under the principal → AgentHome;
  - no arbitrary directory-name scan becomes an identity source.
- [x] Add durable storage-migration metadata and record whether the deployment starts with a configured mutable asset object authority. This is metadata-only in Phase 1; it must not change mirror/hydrate behavior yet.
- [x] Audit user-less/group jobs that currently write under `{base}/agents/{agentID}`. User-less and group Runs get applicable read-only shared Skill roots plus project/scratch; GroupHome and group AgentHome Skills do not become user/user_agent scope.
- [x] Route runner/project/Home setup through registry attachments while the local adapter preserves current physical files; stop treating `agent.workspace` as identity.
- [x] Implement explicit destructive owner deletion with one lock order: acquire and retain stabilized Service admission/publication exclusion, fence matching cached execution, acquire the process-local Home owner gate, then atomically tombstone Homes and delete the owner. Retain both barriers through commit; preserve all physical bytes and permanently reserve identity/locator.
- [x] Add typed user/group isolation, shared Skill-root opacity/read-only attachment, same-raw-ID collision, no-data-move, concurrent Ensure, tombstone permanence, byte preservation, and legacy registration tests.
- [x] Update architecture/developer storage docs and system skill references in English and Chinese for typed Homes and user-less scratch semantics.

**Acceptance:**

- [x] `mise run db:validate && mise run generate:check` exits 0.
- [x] Targeted Home/agent tests prove user `abc` and group `abc` resolve to disjoint Homes, concurrent Ensure creates one row/location, and existing bytes/inodes are not copied or renamed.
- [x] A destructive user, group, or Agent deletion fails without DB mutation if fence-lease acquisition fails. Deterministic cold-runner tests prove an admission already constructing a WorkspaceView completes before deletion, deletion then fences and commits without deadlock, and later admission fails. Cancellation while waiting on sorted Service or publication barriers releases partial acquisition. Concurrent production `SyncAgent` starts publish one Service, close the loser while structurally fenced, and synchronize the winner to the newest snapshot. Removal, shutdown, and Agent commit keep exact Services discoverable and fenced through terminal Runtime close; transaction rollback keeps the Agent Service published and usable. Physical bytes and inodes remain unchanged.
- [x] A user-less Run cannot resolve PrincipalHome/AgentHome, sees only applicable read-only shared Skill roots, and writes only to project/disposable scratch; group AgentHome Skills do not leak into user_agent scope.
- [x] `mise run format && mise run build && mise run test` exits 0.
- [ ] `mise run system-test` exits 0 on a supported host. The Phase 1 handoff attempt ran with embedded PostgreSQL but failed because this orb does not provide a functional Bubblewrap sandbox; webhook, scheduled-run, and drain journeys could not construct local sessions.

Phase 1 deliberately leaves orphaned bytes after destructive owner deletion. It contains no physical purge, purge retry CLI or River worker, maintenance lease, or Store cutover. This is the safer boundary until `sandbox/filesystem-boundary` provides provider-native physical operations and fencing. The dedicated `storage/home-physical-purge` Draft under Issue #828 owns asynchronous destruction, retry/audit, and offline Store migration after that boundary; it must remain Draft until explicitly authorized.

### Phase 2: Filesystem boundary and host-path removal

**Why after Phase 1:** Filesystem operations need typed attachments before host coordinates can be hidden.

The known Project Skill HTTP host-path gap remains implementation work in CherryHQ/stella#928. It must close against this Filesystem/Home boundary and does not relax or redesign the Sandbox v2 target.

- [ ] Define canonical sandbox paths and `internal/fsops` operations for stat/list/read/write/mkdir/remove/rename/upload with root containment, symlink handling, permission preservation, bounded reads, and streaming payloads.
- [ ] Add restricted multicall modes for `stella-fs` and `stella-exec` in the existing `stellad` binary; install digest-matched entry points in the sandbox image/system bundle.
- [ ] Implement local/`none` direct-library adapters and Docker one-shot exec adapter; classify disconnects and writes as outcome unknown without transparent retry.
- [ ] Implement managed Skill publication in `stella-fs`:
  - write/fsync/verify immutable `.stella-revisions/<name>/<tree-digest>` content;
  - atomically flip one canonical contained relative symlink;
  - accept ordinary directory Skills with normal POSIX semantics;
  - reject absolute/escaping/cyclic symlinks and never rename over a non-empty directory;
  - retain old revisions without GC until Phase 3 can prove AgentRun references.
- [ ] Make the temporary no-GC ceiling observable through the existing OTel pipeline: export retained revision count/bytes/oldest-age aggregated by scope, evaluate documented capacity thresholds per root, and emit a structured warning with opaque root identity on breach. Do not put principal/root IDs in metric labels.
- [ ] Change `pkg/sandbox.Session` and policy types so public consumers receive sandbox paths/attachments, never host paths; retain any local resolver only as Provider-private implementation.
- [ ] Migrate Agent `read`, `write`, `edit`, prompt file reads, filesystem Skill catalogs/loaders, and every `ResolvePath`/`ResolveWritePath` caller to Filesystem. Catalog parsing accepts ordinary directories and managed symlinks while skipping `.stella-revisions`.
- [ ] For isolating Providers, mount exact read-only sources at `/opt/stella/skills/builtin`, `/opt/stella/skills/system`, and `/opt/stella/skills/system-agent`; these are execution views, not authority. Managed descriptors resolve/pin the contained exact revision path so a symlink flip affects only a later Turn/Run. `none`/non-isolating local returns exact Provider paths through `SkillView`.
- [ ] Add trusted structured admin writes for SystemSkillRoot/SystemAgentSkillRoot through one-shot Home access. The provisioner accepts validated paths/content and expected digest, never model-authored commands, arbitrary shell, or AgentRun secrets.
- [ ] Route Workspace requests for the exact URL Session through the existing single-process Session lifecycle so it creates/reuses a Sandbox and invokes only Filesystem/`stella-fs`, with no direct host path and no AgentRun secrets. Durable generation, `BeginUse + Open`, keepalive renewal, and cross-replica linearization remain Phase 3 work.

> Make Workspace requests execute `BeginUse + Open` on the exact URL Session generation in Phase 2.

**Review (Fable):** SessionSandbox generation, keepalive, BeginUse, and Provider Open do not exist until Phase 3, so the initial Phase 2 could not land or pass independently.

**Resolved:** Phase 2 now cuts only the file-operation boundary using the current lifecycle; all durable lifecycle and cross-instance acceptance moved to Phase 3.

- [ ] Update the Session OpenAPI first:
  - remove host `root` from Workspace responses;
  - document canonical sandbox paths;
  - keep `scope=user` as a compatibility alias for shared PrincipalHome;
  - regenerate Go and TypeScript clients before server/Web changes.
- [ ] Move host-side parsing of untrusted Sandbox files behind the Sandbox/Filesystem boundary, including the current vision/xberg path, or prove the input is immutable trusted media and record that narrower boundary.
- [ ] Implement the operator-only `stellad storage migrate-assets` command through the Home/blob service layer, following CLI rules:
  - require old writers to be stopped and retain the legacy blob configuration;
  - support dry-run and `--json`, progress on stderr, and actionable non-zero errors;
  - list only mutable user/group asset keys, validate typed principal/path mapping, and write missing content safely into PrincipalHome;
  - verify key count, byte size, and content digest; record the migration marker by PostgreSQL CAS;
  - be idempotent and never delete remote objects.
- [ ] Fail server startup when the durable metadata/config says legacy mutable object authority requires migration but the completion marker is absent. No-authority deployments record/observe `not_required` and continue normally.
- [ ] After the marker gate, make `$STELLA_ASSETS_DIR` ordinary PrincipalHome data; migrate upload/share/channel consumers; remove mutable `asset.Store` object mirroring, hydrate, rollback, and object-version fencing while retaining immutable session media and share snapshots.
- [ ] Extend canonical filesystem Skill metadata to preserve active/deprecated status, `disable_model_invocation`, nested metadata/`created_by`, source/install timestamps, and legacy lifecycle version; scope remains attachment-derived and rename remains delete+create.
- [ ] Add operator-only `stellad storage migrate-skills --dry-run|--json` and a fail-closed Skill authority marker:
  - require maintenance mode, old writers stopped, and complete PG backup;
  - map active rows to exact SystemSkillRoot/SystemAgentSkillRoot/UserHome/AgentHome scope roots;
  - export deprecated rows and `skill_changelog` into a non-catalog operator archive;
  - verify every row/file/path/owner/metadata/byte digest/collision and produce a finite unsupported-row report;
  - default PG files to `0644` unless canonical metadata proves executable intent;
  - write the marker by CAS only after every row has a verified active/archive disposition and Reflect create/patch/delete/runtime-touch/usage adversarial tests pass;
  - support dry-run/idempotent rerun and never delete PG backup/current rows during migration.
- [ ] Preserve Reflect under filesystem authority:
  - migrate `created_by=reflect` and lifecycle metadata;
  - replace integer version CAS with expected canonical tree digest for managed writes;
  - migrate `skill_usage` from `skill.id` FK to logical principal/Agent/scope/name plus last content digest;
  - retain durable worker authorization, runtime usage touch, pair-activity and usage rechecks;
  - make stale/manual concurrent edits conflict, outcome-unknown writes non-retryable, and remove-marker conversion explicit.
- [ ] After the marker, fail startup on residual PG-only mutable current state; remove PG SkillStore/materializer/changelog writers and all runtime readers. Drop obsolete rows/tables only in a later explicit migration after backup retention and last dependency removal.
- [ ] Delete public `ResolvePath`, `ResolveWritePath`, `HostPath` policy leakage, sessionless host `os.*` shortcuts, and obsolete resilient file retry paths.
- [ ] Add one Filesystem conformance suite and run it against local, `none`, and Docker adapters, including permissions, rename, symlink escape, concurrent writes, helper termination, and outcome-unknown writes.
- [ ] Update user/developer docs and built-in skills in English and Chinese for Workspace paths, mutable assets, and removed host-path behavior.

**Acceptance:**

- [ ] `mise run generate:check` exits 0 after the spec-first API/client change.
- [ ] The shared Filesystem conformance suite passes for local, `none`, and Docker; a helper killed during write returns outcome unknown and the caller does not retry.
- [ ] Managed Skill publication tests prove concurrent readers see complete old or new trees without ENOENT/mixed files; unsupported symlink/Store behavior fails conformance. Ordinary CLI mutation retains documented POSIX behavior.
- [ ] A fixture publishes known-size revisions into multiple roots: the collector reports exact aggregate count/bytes/oldest-age, a configured low threshold emits the expected opaque-root warning, and alert/capacity-response documentation is executable without high-cardinality metric labels.
- [ ] Isolating-provider tests prove the three exact `/opt/stella/skills/*` views are read-only and disjoint; a pinned managed descriptor continues reading its old revision after a flip while the next catalog snapshot reads the new revision.
- [ ] Workspace list/read/write/upload succeeds through an exact Session with no warm Sandbox using the current single-process lifecycle; no Workspace path or response exposes a host coordinate.
- [ ] `rg "ResolvePath|ResolveWritePath|HostPath" pkg/sandbox internal/agent internal/server plugins/sandbox` shows no public/caller dependency; any Provider-private resolver is explicitly scoped and tested.
- [ ] A fixture containing assets only in the configured S3-compatible authority fails server startup before migration; dry-run changes nothing; the real migration materializes and digest-verifies every fixture into the correct UserHome/GroupHome, records the marker, is idempotent, and leaves remote objects intact.
- [ ] After cutover, mutable assets written by bash are immediately visible to Workspace/share without an object-store commit or hydrate step; immutable media/share tests remain green.
- [ ] Skill migration fixtures cover active, deprecated, manual, Reflect, metadata-rich, binary, colliding, invalid-path, and unsupported-status rows. Dry-run changes nothing; real migration verifies every disposition; marker-before/after has exactly one authority; residual PG-only state blocks startup.
- [ ] Reflect filesystem tests cover expected-digest conflict, exact retry/outcome unknown, manual concurrent edit, runtime usage touch, pair-activity delete refusal, logical usage migration, and no PG changelog write after cutover.
- [ ] A checked architecture-boundary test proves post-marker production wiring cannot construct/use PG SkillStore, `materializeDBSkill`, or changelog writers; migration/archive-only code is isolated behind the pre-marker operator path.
- [ ] `mise run format && mise run build && mise run test && mise run system-test` exits 0 on a supported host.

### Phase 3: Durable SessionSandbox, AgentRun, input, and reconciliation

**Why before multi-replica:** all correctness currently hidden in one process must move to PostgreSQL while the deployment is still forced to one replica.

- [ ] Add migrations and sqlc queries for:
  - SessionSandbox generation/spec/ref/lifecycle/provisioning intent/keepalive/fencing errors;
  - AgentRun state, executor boot ID, lease, heartbeat, execution marker, abort, terminal/error fields, and partial indexes;
  - `ctx_chat_input` FIFO/receipt/barrier/batch/retry fields and source uniqueness;
  - `ctx_session_inbox` nullable `run_id`, explicit admission/recovery terminal states, a partial unique index preventing multiple receipts from linking to one Run, and checks requiring Run-associated rows to be linked while failed/transcript-recovered rows are unlinked;
  - narrowed expiring GroupRoute claim and atomic responder materialization;
  - `run_id` links on transcript and every source-domain row that needs terminal fold/reply state.
- [ ] Evolve `pkg/sandbox` and local/`none`/Docker Providers to deterministic Provision, Open, Inspect, Destroy, normalized spec/ref validation, endpoint identity, immutable labels, and generation mismatch rejection.
- [ ] Persist provisioning intent before external create; make create/open/destroy idempotent across success, timeout, crash, and ambiguous daemon responses; replace `owner_pid` cleanup with managed-label audit and DB comparison.
- [ ] Implement the Sandbox lifecycle reconciler for expired running Runs, persisted `fencing`, timed-out provisioning intents, and idle Sandbox reaping. Require lifecycle-ready state before any new admission even after the Run partial unique is released.
- [ ] Implement `internal/agent/run`:
  - 90-second lease and 20-second heartbeat using PostgreSQL time;
  - conservative monotonic deadline calculation;
  - guarded `execution_started_at` before the first model/tool/Sandbox operation;
  - ownership CAS before transcript, memory, source-domain, and terminal writes;
  - durable `/abort` request with transactional wakeup and heartbeat fallback;
  - terminal classification and source-domain notification.
- [ ] At admission, read one effective filesystem/builtin catalog plus canonical AgentSkillPolicy, persist their content/policy digests on AgentRun, and keep that snapshot for the complete Run. A policy or symlink flip affects only a later Run; disabled winners never reveal lower same-name entries.
- [ ] Add managed Skill revision GC only after durable Run state exists: retain the current target and every revision that could be observed by an active matching Run; reclaim only noncurrent revisions after no matching Run reference plus a documented grace. Phase 2 disk-growth metrics remain the safety ceiling until this lands.
- [ ] Move Chat, agent-originated Session send, Webhook, Scheduler, Goal, and Delegate to AgentRun as their only running lease; keep only source receipt/queue/budget/fold/retry policy. Remove Goal's “heartbeat failure logs and continues” behavior.
- [ ] Reconcile PR #940 Session activity state: keep `last_turn_started_at`, `last_turn_completed_at`, `last_turn_result`, and `last_viewed_at` as durable presentation watermarks, but replace `SessionRunning`/`SessionLive` as the authoritative state with a query for an unexpired running AgentRun. Write Run-derived started/completed/result metadata in admission or under AgentRun ownership CAS; keep `last_viewed_at` as an independent authorized write.
- [ ] Reconcile PR #962 agent-originated send:
  - retain `ctx_session_inbox` as the exact source/target/provenance receipt and transcript-recovery record rather than copying it into `ctx_chat_input`;
  - atomically persist/validate the receipt, admit and link one AgentRun, and project the inbox input; busy/no-capacity/canceled-before-admission reaches an explicit failed receipt without durable queueing;
  - keep the successful call synchronous, classify process death through the linked Run, and never replay model/tool execution;
  - limit startup recovery to idempotent transcript append/terminalization for legacy or unassociated pending receipts; recovery never creates a Run;
  - delete `internal/agent/session/turnqueue`, or retain it only as removable local fairness before database admission with no correctness or serialization responsibility.
- [ ] Implement `ctx_chat_input` receipt/admission:
  - validate payload/schema and hard size limits before a row can enter `queued`;
  - fetch platform attachments before ack and persist immutable content-addressed media refs rather than expiring URLs;
  - enforce configurable per-binding, per-principal, and deployment-wide backlog row/byte quotas before acceptance, with protocol-appropriate backpressure/redelivery;
  - stable source dedup and structured duplicate state;
  - canonical binding coordinates and per-binding `BIGINT` lane sequence;
  - current authorization/policy resolution at admission;
  - bounded count/bytes batching over a compatible queue prefix;
  - blocked-lane backoff/observability and audited reject;
  - no replay after an interrupted Run.

> Implement `ctx_chat_input` with dedup, lane sequence, batching, poison-head handling, and no replay.

**Review (Fable):** The initial task omitted architecture-required pre-queue payload validation, durable attachment conversion, and backlog quotas; an accepted image could expire while queued and a flood could grow PostgreSQL without bound.

**Resolved:** Added all three acceptance-boundary responsibilities and runnable expiry/quota checks.

- [ ] Before deleting `channel_chat_command_receipt`, backfill every historical row by stable source identity into `ctx_chat_input(kind=control, state=handled)`; verify counts/uniqueness, then move `/new` into the lane with receipt-time `expected_session_id` compare-and-rotate.

> Delete or demote command receipts with the old process-local correctness mechanisms.

**Review (Fable):** `channel_chat_command_receipt` is durable protection against destructive `/new` redelivery, not process-local state; deleting it without backfill reopens silent successor-session archival.

**Resolved:** Added the architecture-required handled-control backfill before table deletion and an explicit historical-redelivery regression test.

- [ ] Keep `/abort` out-of-band; remove channel `/agent`, `/model`, `SwitchModel`, related UI/help/docs, and the physical-chat routing requirement.
- [ ] Replace group outbox/dispatch execution with one seq-ordered GroupRoute claim:
  - classification-only expiring claim and safe retry;
  - atomic responder decision plus FIFO materialization;
  - Web local executor reservations;
  - `state=rejected, reject_code=busy` per busy responder while free responders retain local SSE;
  - remove `ctx_group_dispatch`, process `sessionQueue`, and Publisher registry correctness.
- [ ] Make Workspace `BeginUse`, keepalive, idle reaping, AgentRun fencing, and concurrent operations linearizable through the SessionSandbox row.
- [ ] Change the Session API spec first:
  - require/accept stable `client_message_id` for first-party sends;
  - add AgentRun Get and abort custom method;
  - add structured duplicate, busy, recovering, and remote-live error details;
  - make `/events` return 204 only with no valid active Run and 503 for a remote valid Run.
- [ ] Update the Web UI to create stable message UUIDs, poll AgentRun plus transcript every three seconds on remote-live fallback, and render completed/failed/cancelled/interrupted/recovering states.
- [ ] Delete or demote process-local correctness in runner cache, active-turn map, SessionHub, channel queue, Session `turnqueue`, old group leases, and resilient recreation. Delete the old command receipt table only after its handled-control backfill is verified. Caches, hubs, and local fairness may remain only as optional acceleration.
- [ ] Keep all Docker/Kubernetes multi-replica feature gates closed at the end of this phase.

**Acceptance:**

- [ ] `mise run db:validate && mise run generate:check` exits 0; partial unique/check constraints reject two running Runs, multiple Session inbox receipts linked to one Run, admitted inbox rows without a Run, linked failed/recovered rows, invalid input source combinations, and duplicate stable source identities.
- [ ] Deterministic DB integration tests with two independent service instances prove one admission winner, heartbeat/reaper linearization, abort/completion ordering, stale durable-write rejection, `/new` compare-and-rotate, and GroupRoute seq order.
- [ ] Two independent service instances concurrently handling agent-originated sends to the same target Session prove exactly one running AgentRun and no concurrent model/tool/Sandbox execution; the loser receives explicit busy/failed outcome.
- [ ] Agent-send crash tests prove process death after admission produces an interrupted linked Run with no replay, while a legacy/unassociated pending receipt is appended to the transcript exactly once without executing a turn.
- [ ] Agent-send transaction rollback/crash leaves no current-format recovery-pending orphan; busy/no-capacity commits only a failed, unlinked receipt.
- [ ] The same agent-send integration suite passes with `turnqueue` absent; if a local fairness queue remains, bypassing it does not weaken any invariant.
- [ ] After a replacement Run starts, a stale executor cannot overwrite its `last_turn_started_at`, `last_turn_completed_at`, or `last_turn_result`; an authorized viewed update remains independent.
- [ ] Crash-window tests cover: commit-before-start, marker-before-call, provisioning create timeout, crash after fencing CAS, destroy retry, stale executor completion, and Workspace write interrupted by fencing.
- [ ] Workspace tests with two independent service instances prove `BeginUse + Open`, keepalive/reaper ordering, exact-generation validation, and ordinary POSIX concurrency with a Run on the other instance.
- [ ] Skill tests prove an AgentRun keeps its admitted catalog/policy digest across concurrent admin/CLI changes; the next Run sees the change; GC never removes a revision observable by an active Run.
- [ ] FIFO tests prove bounded compatible batching, no debounce, no skip across barriers/poison heads, per-responder Web busy rejection, group multi-agent fan-out, and no automatic replay after interruption.
- [ ] A historical consumed `/new` receipt is backfilled as handled; redelivering the same platform message after cutover does not rotate or archive the successor Session.
- [ ] An accepted image remains readable after its platform URL expires because the queued input references immutable media; inputs exceeding binding/principal/deployment row or byte quota are rejected/backpressured before ack.
- [ ] Subprocess system journeys prove durable abort, executor process death → interrupted/fencing/recovery, remote `/events` semantics, and final transcript polling without relying on a process-local hub.
- [ ] Startup rejects any multi-replica flag even though the durable runtime is present.
- [ ] `mise run format && mise run build && mise run test && mise run system-test` exits 0 on a supported host.

### Phase 4: Shared control state and Docker Compose multi-replica gate

**Why before Kubernetes:** this proves Stella's distributed protocol with fewer platform variables; Kubernetes should only add a Provider and storage topology.

- [ ] Implement one pool-external serialized PostgreSQL control session per replica for abort/input/config wakeups, health probes, and the global channel advisory lock; reject transaction-pooling proxies.
- [ ] Make pull/WebSocket channel startup globally single-leader:
  - control-session loss cancels all listeners;
  - graceful drain stops listeners before releasing the lock;
  - webhook ingress remains stateless;
  - provider cursor/ack advances only after durable ingress commit.
- [ ] Make every Publisher reconstructable from durable channel config, bot identity, reply envelope, cursor/recovery metadata, and encrypted capability references; remove dependency on leader-local clients/registries.
- [ ] Persist connection OAuth device/auth-code flows with encrypted secret material, expiry, and one-shot CAS; preserve signed-cookie browser OIDC without duplicating state.
- [ ] Replace process-local login/registration/authorization rate limits with PostgreSQL-time atomic state keyed by a privacy-preserving hash; fail closed on DB unavailability.
- [ ] Add monotonic config revisions and transactional `{kind,id,revision}` invalidation; startup/reconnect/cache-miss reloads from PostgreSQL, and missed notification cannot preserve authorization.
- [ ] Treat the canonical AgentSkillPolicy digest as its durable revision. Dedicated policy mutation locks the Agent row, updates only the reused JSONB column, and transactionally notifies `{agent_id, policy_digest}`; missed notification is repaired by admission re-read/digest validation.
- [ ] Serialize cross-replica managed Skill/Reflect writers for one logical ref with a bounded PostgreSQL advisory-lock connection held only around expected-digest verification and `stella-fs` publication. Lock loss/outcome unknown fails closed; arbitrary POSIX writers remain outside this managed guarantee by design.
- [ ] Add a dedicated `test/compose/` three-replica topology:
  - named `stellad-a/b/c` services and explicit host ports;
  - shared external PostgreSQL, Docker socket/daemon, PrincipalHome/AgentHome volumes, image digest, and no test load balancer;
  - isolated project/volume names and deterministic cleanup so developer data is never touched.
- [ ] Add `mise run compose-test` and a Go/shell harness that drives and inspects replicas directly. Cover at least:
  - Run initiated on A, Workspace operation on B, abort from C;
  - concurrent agent-originated sends from A and B to one target Session, with one AgentRun winner and no local-queue dependency;
  - executor container/process kill, lease expiry, fencing, resource replacement, and later Run on another replica;
  - queued ChatBinding input, batching, `/new`, duplicate delivery, GroupRoute, and poison-head recovery;
  - one channel leader, control-session failure, leader handoff, cursor redelivery dedup, and outbound publish from a non-leader executor;
  - remote read-only attach 503 plus AgentRun/transcript polling;
  - OAuth flow continuation, global rate limit, and config invalidation across replicas.
  - policy changed on A invalidates B/C and affects only the next Run; two replicas racing a managed Reflect patch have one expected-digest winner; ordinary POSIX race follows documented winner semantics without corrupting a revision tree.
- [ ] Add adapter-level fake platform tests where real credentials are inappropriate; do not add production debug endpoints or a new broker solely for tests.
- [ ] Open the explicit Docker/Compose multi-replica flag only after the suite passes; startup still rejects independent daemons, embedded PostgreSQL, transaction pooling, unsupported volume-subpath, or non-reconnectable Home Stores.
- [ ] Document the shared-daemon Docker topology honestly: horizontally scalable control plane, one execution failure domain.

**Acceptance:**

- [ ] `mise run compose-test` provisions a clean three-replica stack, runs every cross-replica/crash journey, and tears it down without modifying the ordinary `stella-data` developer volume.
- [ ] At every observation point PostgreSQL contains at most one running Run per Session and one channel ingress leader; no old container survives a completed fencing transition.
- [ ] Every replica rejects stale AgentSkillPolicy digest at admission even after a dropped notification; cross-replica managed Skill writers never publish a mixed tree or both claim the same expected digest.
- [ ] Killing the executor and separately killing the channel leader both recover without duplicate execution or lost accepted input; unknown side effects remain interrupted rather than replayed.
- [ ] A non-leader replica can publish a reply using only durable state; Weixin-style capability values never appear in argv, logs, Pod/container spec, Run rows, or reply JSON.
- [ ] Supported shared-daemon configuration starts with the multi-replica flag; each unsupported configuration fails startup with a specific actionable error.
- [ ] `mise run format && mise run build && mise run test && mise run system-test && mise run compose-test` exits 0.

### Phase 5: Kubernetes Provider and cluster conformance

**Why last:** the distributed protocol is already proven; this phase is allowed to fail only for Kubernetes-specific compute, storage, scheduling, and security reasons.

- [ ] Add the minimum official Kubernetes client dependencies required for Pod/PVC/exec/watch operations; record the dependency escalation and keep Kubernetes code inside `plugins/sandbox/kubernetes` plus its HomeStore adapter.
- [ ] Implement Kubernetes Provider lifecycle with deterministic Pod names, immutable UID refs, normalized spec labels, idempotent Provision/Open/Inspect/Destroy, watch/poll recovery, and no ServiceAccount token in Session Pods.
- [ ] Implement the PrincipalHome RWX Pool adapter:
  - mandatory existing claim or explicit StorageClass/capacity;
  - opaque typed PrincipalHome/SystemSkillRoot/SystemAgentSkillRoot subpaths;
  - one-shot trusted provisioner Pod for Ensure/Purge/POSIX conformance and structured authenticated admin Skill writes;
  - root containment, no model input/secrets, and crash cleanup.
- [ ] Implement one topology-aware RWO PVC per AgentPlacementKey:
  - `WaitForFirstConsumer`, explicit capacity, expansion support, and proven purge semantics;
  - stable Home label and required same-host Pod affinity;
  - no Session/Pod owner reference;
  - scheduler/CSI attach limits plus namespace ResourceQuota.
- [ ] Build Session Pod specs from the same digest-pinned image/system bundle as Docker; mount only the exact PrincipalHome/AgentHome plus applicable read-only SystemSkillRoot/SystemAgentSkillRoot attachments under the three canonical `/opt/stella/skills/*` views. Do not materialize DB Skills into scratch.
- [ ] Implement one-shot `stella-exec` env delivery over bounded stdin so OAuth refresh affects later processes without writing secret values into PodSpec, SandboxRef, labels, logs, or PVCs.
- [ ] Enforce the security baseline in Provider and Helm:
  - dedicated-node guidance;
  - non-root, drop-all capabilities, seccomp, AppArmor/SELinux where available;
  - no host namespaces, hostPath, socket, or auto-mounted ServiceAccount token;
  - default-deny egress, explicit resource requests/limits, narrow RBAC.
- [ ] Replace the chart's single RWO `STELLA_HOME` model with explicit external PostgreSQL, PrincipalHome RWX, AgentHome RWO, Provider, security, quota, and capability-gate values. Keep drain + `Recreate`; mixed versions remain unsupported.
- [ ] Add `mise run kubernetes-test` plus `test/kubernetes/` conformance that runs only against an explicitly selected disposable cluster/namespace and never guesses CSI capability.
- [ ] Reuse Phase 4 protocol journeys against multiple `stellad` Pods, then add Kubernetes-only checks:
  - PrincipalHome POSIX semantics across nodes;
  - concurrent first AgentHome placement;
  - same-node Session replacement without needless detach;
  - node loss, old-Pod disappearance, CSI detach/reattach, and no double mount;
  - Pod UID/generation fencing and stale exec rejection;
  - provisioner crash/purge retry;
  - managed Skill symlink-flip visibility across nodes and refusal on a CSI driver that cannot prove it;
  - RBAC, network policy, security context, quota, and no-secret persistence.
- [ ] Open Helm `replicaCount > 1` only after the target cluster's configured CSI and security conformance passes; otherwise chart/runtime validation fails closed.
- [ ] Update README, deployment/storage/sandbox development docs, chart docs, built-in skills, and Chinese equivalents. Explain that Compose proves the protocol while Kubernetes conformance proves the platform.

**Acceptance:**

- [ ] `mise run deploy:helm:check` passes and rendered manifests contain no Session hostPath/socket/ServiceAccount-token mount, include required security/resource settings, and reject incomplete storage/multi-replica values.
- [ ] `mise run kubernetes-test` passes on the declared CSI-backed test cluster, including cross-node RWX POSIX checks, RWO affinity/attach behavior, Pod deletion, node-failure detach/reattach, generation fencing, and provisioner recovery.
- [ ] Shared system/system_agent roots are visible read-only to exactly the applicable Pods; cross-node readers observe complete old/new managed Skill trees, and no DB Skill scratch or Pool-root mount exists.
- [ ] The Phase 4 distributed protocol suite passes unchanged against Kubernetes endpoints; failures are not papered over with sticky routing or owner RPC.
- [ ] Secret scans over rendered PodSpecs, labels, refs, logs, and mounted persistent files find no injected Vault/OAuth values.
- [ ] A targeted Windows cross-build remains green for non-Kubernetes/local code paths, and unsupported local Kubernetes execution fails by configuration rather than build tags leaking behavior.
- [ ] `mise run format && mise run build && mise run test && mise run system-test && mise run compose-test && mise run deploy:helm:check` exits 0; `mise run kubernetes-test` also exits 0 in the declared cluster environment.

## Cross-phase challenge checklist

Before marking any phase complete, attack its diff as:

- **bug hunter:** crash between every database and external-resource step; duplicate requests; nil/empty legacy data; concurrent first-use;
- **security auditor:** authority revalidation, typed principal isolation, secret redaction, path containment, stale executor writes, RBAC/network exposure;
- **architecture critic:** no duplicate authority, no shallow wrapper module, no process cache required for correctness, no new mechanism where PostgreSQL/Provider/stdlib already suffices;
- **correctness prover:** identify every linearization point, CAS predicate, terminal state, idempotency boundary, and unknown-outcome branch.

Any finding that changes safety, product semantics, the selected module boundary, or an Acceptance item reopens the plan. Optional optimization remains deferred with a measurable trigger.

## Review threads

> This plan has one separately reviewed Phase 0 prerequisite followed by five parent phases, with Docker Compose as the hard multi-replica gate before Kubernetes.

**Review (Fable, round 1):** `CHANGES REQUIRED`. The initial draft omitted safe migration of object-only mutable assets and historical `/new` receipts, placed durable Workspace lifecycle work one phase too early, and omitted pre-queue attachment/quota requirements.

**Resolved:** Added D55 plus offline asset migration/startup gate; added handled-control receipt backfill; restored Phase 2/3 lifecycle independence; added payload/media/quota tasks and acceptance. Inline threads above preserve each finding and resolution.

**Review (Fable, round 2):** `DECISION: APPROVED`. Fable re-read the revised plan and architecture rev 7, verified all four round-1 findings were substantively closed, found no new required change, and confirmed the five phases and Acceptance blocks are safe and executable.

**Resolved:** The rev 7 approval gate was satisfied. Skill authority rev 3 later reopened architecture and implementation order before Phase 1 began.

**Review (Fable, Skill authority round 1):** `CHANGES REQUIRED`. Broad Agent updates could erase policy; legacy arrays had invented semantics; Reflect had no cutover; non-empty directory replacement was not atomic; custom filesystem system Skills could disappear; and PG-only status/metadata had no export disposition.

**Resolved:** Rev 8 and this plan stop broad policy writes, preserve current legacy behavior, retain Reflect through filesystem digest identity and logical usage telemetry, use contained revision symlink flip, gate nested legacy roots, and exhaustively archive/migrate metadata.

**Review (Fable, Skill authority round 2):** `DECISION: APPROVED`. Fable found no remaining mandatory issue and approved release bundle + Home filesystem authority, the Phase 0 → Homes → stella-fs → offline cutover → AgentRun → Compose → Kubernetes order, and the expanded acceptance gates.

**Resolved:** The architecture-level Skill gate is satisfied; implementation starts with the separate Phase 0 plan, not Phase 1.

**Review (Fable, exact transcription round 1):** `CHANGES REQUIRED`. The Phase 2 no-GC ceiling had no task or acceptance for disk-growth observability.

**Resolved:** Phase 2 now uses the existing OTel pipeline for bounded-scope retained revision count/bytes/oldest-age, per-root threshold warnings without high-cardinality labels, documented capacity response, and a runnable known-size fixture. Policy revision wording now consistently means canonical digest, and the Skill marker explicitly waits for Reflect adversarial tests.

**Review (Fable, exact transcription round 2):** `DECISION: APPROVED`. Fable verified the architecture, this parent plan, and the byte-identical Phase 0 plan; it found no mandatory issue. It also approved the clarified `/opt/stella/skills/{builtin,system,system-agent}` read-only execution views as coordinates over the same two authorities, not a third source.

**Resolved:** Final review gate was satisfied. After later current-main reconciliations, the repository artifacts are authoritative; historical authoring copies do not override them.

**Decision (V, full-program execution):** Implement all phases, reuse umbrella Issue #828 for every PR, create no child Issues, and open every PR as Draft. V rejected a 23-PR draft as over-granular and explicitly authorized Sol self-review without another Fable pass.

**Review (Sol, four perspectives):** The 15-PR map preserves each material rollback/activation boundary while removing bookkeeping-only splits. Bug review keeps asset marker before Skill cutover, receipt backfill before deletion, and cleanup after both authorities move. Security review keeps Docker/Kubernetes gates closed until their conformance PRs and prevents partial control-state rollout from enabling replicas. Architecture review keeps one vertical Phase 1 Home module, separates the two Phase 2 authority transitions, and refuses fake parallel PRs without independent rollback. Correctness review requires every speculative lane to compile against committed contracts, caps substantive concurrency at two, and gives each phase one final aggregate Acceptance owner.

**Resolved:** Baseline is exactly 15 just-in-time Draft PRs, all `Refs #828`. Split only when implementation evidence exposes another independent rollback boundary or no single runnable Acceptance story remains; revise the plan before changing count. No PR becomes Ready or merges without V's explicit instruction.

## Handoffs

Every completed phase must replace its pending entry with the concrete handoff required by the Blueprint workflow.

### Handoff after Phase 1

- **What landed** — `storage_home`/`storage_migration`, typed `internal/home` Store registry and opaque attachments, legacy registration, explicit consumer injection, local compatibility projection, ready-root inspection, and atomic owner fence/tombstone with synchronized EN/ZH architecture/storage/Skill docs. Tombstoned identities and locators remain permanent while every physical byte is preserved.
- **Acceptance results** — `mise run db:validate`, `mise run generate:check`, `mise run format`, `mise run build`, and full `mise run test` exited 0 for the recorded Phase 1 head. Focused tests prove typed user/group isolation, one-location concurrent Ensure, inode-preserving registration, user-less scratch, shared-root read-only policy, group/Agent overlap tombstone, rollback on deletion failure, byte preservation, exact Home consumer routing, and retained admission-fence ordering for user/group/Agent deletion. The ordering test drives a cold runner factory into the real WorkspaceView owner gate, proves that admission completes first without deadlock, and verifies post-commit rejection. Deterministic production-path tests also cover concurrent `SyncAgent` publication and winner refresh, fenced loser/removal/shutdown Runtime close, shutdown rejection, context cancellation at each retained-fence wait, concurrent deletes, and Agent rollback/unpublish timing. `mise run system-test` started with embedded PostgreSQL but failed because this orb lacks a functional Bubblewrap sandbox; webhook, scheduled-run, and drain journeys could not construct local sessions, so supported-host System Test acceptance remains open.
- **Decisions made during impl** — one injected `home.WorkspaceViewer` is mandatory with no production fallback; Phase-1 local projection uses bounded context-cancellable owner stripes, pinned no-follow ready-root inspection, and a short DB-only revalidation transaction. Owner deletion first retains context-aware Service admission/publication barriers in stable Agent-ID order, then acquires the Home owner gate and keeps both through commit. Removal, shutdown, and rejected concurrent-start candidates retain structural exclusion until terminal Runtime close returns; insertion-only publication never overwrites an existing Service, and an Agent owner service is unpublished only after its successful database commit and Runtime close. A `ready` row never recreates a missing root; missing, non-directory, and symlink roots fail resolution. Retained pins detect replacement only during their bounded revalidation interval, not across operations or restart: Phase 1 has no durable inode identity against trusted host-side replacement, so restore and root cleanup run stopped or with consumers fenced. User-less `runner-scratch` is disposable, never Home authority, and mounts only an exact child under isolating providers; normal close/construction failure cleans best-effort, while crash or trusted host tampering can leave operator-cleaned children. Phase 1 has one immutable configured LocalStore identity; changing it after registry creation fails closed until a future offline migration.
- **Surprises / gotchas** — physical purge before a provider/filesystem boundary required increasingly complex DB claims, host locks, and River continuation semantics without provider-level authority. The accepted reduction preserves orphaned bytes rather than risking destructive deletion. Group and Agent deletion sets overlap on AgentHome, so the second valid owner delete skips the first delete's terminal tombstone rather than failing.
- **What changed from this plan** — Phase 1 now ends at terminal tombstone/fence and does no physical destruction or Store cutover. A dedicated Draft `storage/home-physical-purge` PR under Issue #828 follows `sandbox/filesystem-boundary` and precedes authority cutovers that depend on cleanup. The baseline map therefore grows from 15 to 16 Draft PRs.
- **What remains open** — Phase 1 still needs a successful `mise run system-test` on a supported host. Phase 2 must remove host-path filesystem access and route provider-native operations through opaque attachments before the new purge PR can implement physical cleanup. Mutable asset contents, mutable Skill authority, SessionSandbox, multi-replica, and Kubernetes remain intentionally unchanged.
- **What Phase 2 must read or verify first** — `internal/home/{home,workspace,local,deletion}.go`, `pkg/sandbox.HomeAttachment`, the live consumer AST guard, architecture §5.4/§9, and the invariant that shared Skill consumers cannot move Stores until readers and mounts derive coordinates from attachments.

### Handoff after Phase 2 — pending

- What landed
- Acceptance results
- Decisions made during implementation
- Surprises / gotchas
- What changed from this plan
- What remains open
- What Phase 3 must read or verify first

### Handoff after Phase 3 — pending

- What landed
- Acceptance results
- Decisions made during implementation
- Surprises / gotchas
- What changed from this plan
- What remains open
- What Phase 4 must read or verify first

### Handoff after Phase 4 — pending

- What landed
- Acceptance results
- Decisions made during implementation
- Surprises / gotchas
- What changed from this plan
- What remains open
- What Phase 5 must read or verify first

### Handoff after Phase 5 — pending

- What landed
- Acceptance results
- Final deviations from architecture rev 8
- Residual operational risks and deferred triggers
- Documentation/release status
- Recommended post-implementation review and rollout sequence
