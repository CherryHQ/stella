# Plan: Implement Sandbox Architecture v2

- **Status:** Current execution plan
- **Architecture:** `docs/design/2026-08-01-sandbox-architecture-v2.md`
- **Foundation:** #862 merged as `d05375f4e28b364a5023cdf6e15ccf4b83f9d378`; #886 is the current implementation
- **Sequence:** #862 → #886 → #888 → #897 → optional #928 → shared-POSIX readiness + distributed lifecycle fencing → Compose/Kubernetes conformance

This plan is the active implementation source. It intentionally omits obsolete checklists and does not prescribe a fixed full-program PR count or future branch names.

## Outcomes

The program is complete when:

1. Every mutable durable Workspace/filesystem consumer resolves an authorized, typed, deterministic POSIX root through `WorkspaceManager`; immutable bundle and BlobStore/S3 consumers keep their own authorities.
2. Single-replica deployments use one local POSIX `$STELLA_HOME` without changing the application storage model.
3. Multi-replica deployments use one globally shared, strongly consistent POSIX namespace and pass deployment conformance, benchmark, and mount-readiness gates.
4. PostgreSQL generation/lease fencing plus each replica's local lifecycle gate protects AgentRun and Session compute independently of workspace location.
5. Workspace/API access is independent of Session compute and never acquires AgentRun or Sandbox generation.
6. Mutable Skill bytes are authoritative in deterministic POSIX roots; builtin Skills and immutable media/blob/share snapshots retain their immutable authorities.
7. Compose and Kubernetes prove the same contracts before multi-replica activation.

## Invariants for every change

- PostgreSQL remains authority for business identity, owner, authorization, configuration, receipts, and control metadata. It does not identify workspace directories; mutable filesystem bytes remain in POSIX roots. This does not prohibit PG from storing messages or other business content.
- S3 and `BlobStore` remain supported for immutable blobs, artifacts, media, and share snapshots. S3 is not the live workspace API.
- Concurrent workspace changes use ordinary POSIX semantics. No transparent retry follows an outcome-unknown write or external side effect.
- `SandboxRef.generation` identifies compute only; it cannot locate durable files.
- Owner deletion retains bytes. Optional future cleanup is stopped/fenced operator maintenance, never an online workspace lifecycle state machine.
- API changes remain OpenAPI-first, and tests use the lowest sufficient layer.

## Active sequence

### 1. #862 — WorkspaceManager foundation (merged)

Implemented scope:

- sole injected `WorkspaceManager` and typed deterministic user/group/principal-Agent/global-Agent roots;
- durable owner validation before materialization;
- pinned root-FD, `openat`, no-follow containment and wrong-kind rejection;
- one process-local lifecycle gate shared by production services;
- owner deletion fencing with bytes/inodes retained;
- filesystem occupancy preventing global Agent-ID reattachment;
- consumer routing needed to make the manager the production authority.

Acceptance evidence includes focused path, owner, concurrency, deletion, retention, cancellation, and occupancy tests, plus successful format/build/unit gates recorded in the #862 handoff. One item remains open: run `mise run system-test` on a supported host. The orb attempt could not construct Bubblewrap sessions, so do not mark that gate accepted from current evidence.

#862 also removed the old active direction: no `HomeStore`, `HomeAttachment`, `storage_home`, `storage_migration`, Store ID/locator, ready/tombstone state, physical purge, or `storage/home-physical-purge` work was introduced.

### 2. #886 — Rooted POSIX operations and mount boundary

Implement a small operation set over an already authorized `WorkspaceManager` root: stat/list/read/write/mkdir/remove/rename/upload as required by current consumers.

Required work:

- define canonical root-relative paths and an error taxonomy;
- materialize typed root components with root-FD-relative no-follow traversal, then operate through an inode-pinned contained root that permits ordinary relative symlinks only when they stay inside it;
- enforce root containment and read-only roots;
- support bounded reads, streaming large payloads, modes, atomic same-directory rename where requested, and explicit durability behavior;
- classify write failures after possible mutation as outcome unknown and prove callers do not retry;
- expose exact authorized roots to Sandbox providers as POSIX mount views;
- add shared operation and mount conformance for local, Docker, and later Kubernetes;
- keep Workspace/API direct access independent of Session lifecycle.

Acceptance:

- traversal, symlink escape, root replacement and read-only writes fail closed;
- create permissions, append, same-root rename, optional fsync, operation limits, and active-operation owner fencing meet the declared contract;
- large files stream without fixed capture-buffer truncation;
- a killed/disconnected write is reported as unknown and not replayed;
- an isolating Sandbox sees only its exact authorized views; the explicit `none` backend remains trusted-host execution;
- a Workspace request succeeds with no AgentRun and no warm Session compute.

Do not implement a per-Session filesystem transport, `stella-fs`, helper/image protocol, or file-access compute lifecycle.

### 3. #888 — Route durable file consumers

Route each mutable filesystem consumer through business authorization, `WorkspaceManager`, and #886 rooted operations.

Required consumer audit:

- Workspace API and Web UI;
- Agent `read`, `write`, and `edit` tools;
- prompt and channel file reads/writes;
- share publication;
- mutable assets and other direct host-path callers.

Immutable session media, content-addressed blobs, artifacts, and published share snapshots remain in `BlobStore`/S3. Share creation reads an authorized filesystem version and emits an immutable snapshot.

For legacy mutable assets, first gather evidence by deployment/configuration and key shape. Add an offline, idempotent, asset-specific migration only if object-only mutable data exists. It must verify principal mapping, path, count, size and digest, preserve the old object copy, and fail closed on incomplete migration. Do not generalize this into a workspace migration/catalog.

Acceptance:

- an authorization failure causes no filesystem operation;
- no audited caller constructs or exposes a durable host path;
- bash/CLI and API mutations become mutually visible through ordinary POSIX semantics;
- immutable media/blob/share tests remain green;
- any asset migration has fixtures proving dry-run, idempotency, complete verification and no remote deletion; if no migration is added, the evidence for exclusion is recorded.

### 4. #897 — Mutable Skill filesystem authority

Make mutable Skill bytes authoritative in deterministic POSIX roots. Keep builtin Skills in the immutable release bundle.

Required work:

- map each mutable Skill scope to a typed authorized root and derive scope from that root;
- route catalog, prompt/search/load/file, admin mutation and Reflect consumers through #886 operations;
- preserve policy in PG and retain only narrowly justified migration state and Reflect business telemetry;
- define any required PG-to-filesystem cutover as a Skill-specific, offline, verified transition with one authority before and after;
- preserve ordinary directory and CLI POSIX behavior by default;
- use temp write, required `fsync`/close, and same-directory rename for managed updates where sufficient;
- add revision symlink/CAS/GC only if a concrete Skill-domain requirement and conformance test independently justify it.

Acceptance:

- builtin bytes are read only from the pinned bundle;
- mutable catalog/content reads observe filesystem authority with no PG content mirror or restore-on-miss;
- policy changes do not overwrite content, and content changes do not redefine authorization;
- ordinary POSIX edits are immediately visible under the documented consistency contract;
- Reflect authorization and telemetry survive without becoming a second content authority;
- migration fixtures, if required, cover metadata, binary files, collisions, invalid paths, manual content and Reflect content with exactly one authority at each side of cutover.

### 5. Optional #928 — Residual path cleanup

After #886, #888 and #897, audit for legacy path APIs and callers. Remove only residual compatibility surfaces and tests that no longer have a consumer. Fold this work into the preceding change or drop #928 if there is no independent rollback boundary.

Acceptance is an explicit repository audit showing no production caller bypasses authorization plus `WorkspaceManager` rooted operations. Do not keep a PR solely to satisfy the issue-number sequence.

## Multi-replica convergence

After the consumer and Skill sequence, two workstreams may proceed independently. Multi-replica stays disabled until both converge.

### A. Shared-POSIX readiness

Define the deployment contract without making a specific filesystem a Stella dependency.

Required work:

- document one global namespace visible at identical logical roots to every `stellad` replica and Session compute;
- provide a backend-neutral conformance suite for rename, symlink, permissions, locking, append, concurrent read/write, close-to-open consistency and fsync durability;
- benchmark representative metadata, small-file, large-file and concurrent workloads with documented pass criteria;
- implement startup/readiness probes for mount existence, identity, read/write capability and freshness;
- fail closed on mount loss or semantic mismatch;
- publish JuiceFS CE as a recommended implementation while permitting EFS/NFS/CephFS/etc. that pass the same gates.

Acceptance records the backend, topology, mount options, conformance result, benchmark result and failure/readiness behavior. A Compose volume or successful Pod mount alone is insufficient.

### B. Distributed lifecycle fencing

Move cross-replica execution correctness to PostgreSQL generation/lease state while preserving every replica's local gate.

Required runtime contracts:

- one running `AgentRun` per Session, bound to one executor boot identity and never transferred;
- Chat, agent-originated send, channel, Webhook, Scheduler, Goal and Delegate use AgentRun as their only execution lease;
- PostgreSQL-time heartbeat and ownership CAS; operation boundaries check the guard before model, tool and Sandbox operations;
- durable abort that races linearly with completion; notification is only a wakeup;
- heartbeat/lease expiry makes the Run visibly interrupted. Every Run-owned transcript, memory, source-domain, terminal and Run-derived Session activity write verifies current `run_id + executor_boot_id + running` ownership in the same PostgreSQL transaction or guarded CAS that commits the write; `last_viewed_at` remains an independent authorized presentation write;
- executor loss or compute-operation uncertainty increments compute generation, destroys the old resource, then permits replacement;
- no transparent retry of unknown model/tool/filesystem/publish effects;
- Session activity/viewed metadata remains presentation state;
- Workspace API does not participate in Run admission, lease, compute generation, keepalive or recovery.

Preserve channel/runtime contracts:

- `ctx_session_inbox` remains the exact source/target/provenance receipt and transcript-recovery record for agent-originated send. Live admission atomically persists or validates it, creates and links at most one AgentRun, and projects the input. Busy/no-capacity/cancel-before-admission commits only a failed unlinked receipt and never queues. Process death follows the linked Run without replay; startup may append/terminalize only legacy or unassociated receipts and never creates a Run or invokes model/tools. Any local `turnqueue` is optional fairness only;
- durable per-ChatBinding FIFO with stable-source deduplication, bounded compatible batching and barriers. Payload/schema/hard-size validation, conversion of expiring attachments to immutable content-addressed media, and binding/principal/deployment row+byte quotas happen before ack;
- poison heads are never auto-skipped or silently dead-lettered. Transient failure uses capped backoff and blocked-lane observability; progress past a poison head requires explicit audited rejection;
- `/new` is an ordered barrier whose receipt stores `expected_session_id` or binding revision and compare-and-rotates once. Historical consumed command receipts are backfilled before deleting the old receipt authority;
- one seq-ordered, expiring, classification-only `GroupRoute` claim without a second Agent execution lease. The winner atomically persists responder decisions and unique FIFO materialization; stale claimants cannot commit. Web fan-out records explicit busy rejection per responder while accepted responders retain local SSE;
- exactly one pool-external, serialized PostgreSQL control session per replica handles abort/input/config wakeups, health checks and the global pull/WebSocket advisory lock. Transaction-pooling proxies are rejected; session loss immediately cancels listeners; graceful drain stops listeners before releasing leadership; notifications are wakeups repaired by heartbeat, reconnect and full scan;
- durable cursor/ack behavior and a Publisher reconstructable from durable config, reply envelope and encrypted capability reference. Non-leader executors can publish; Webhook remains stateless;
- local primary SSE; remote live attach returns structured `503` and clients poll durable Run/transcript state; `204` means no active Run.

Acceptance uses independent service instances and crash windows to prove one admission winner, heartbeat/reaper ordering, abort/completion ordering, transaction-coupled stale-write rejection after replacement, old-resource destruction before replacement, and no replay. Channel acceptance covers attachment expiry, quota-before-ack, poison-head/barrier ordering, historical `/new` redelivery, stale GroupRoute claimant rejection, atomic/partial-busy responder materialization, control-session loss and ordered handoff, missed-notification recovery, transaction-pooling rejection, and non-leader Publisher reconstruction.

## Compose and Kubernetes conformance

These gates begin only after shared-POSIX readiness and distributed lifecycle fencing both pass independently.

### Compose

Use multiple named replicas with external PostgreSQL and one shared POSIX namespace. Exercise replicas directly rather than relying on sticky routing. Cover cross-replica Run admission, Workspace concurrency, abort, executor death, channel leader failover, Publisher reconstruction, FIFO/GroupRoute, and local-SSE/remote-polling behavior.

Compose proves a deployment of the contracts; it does not replace shared-filesystem conformance, benchmarks, readiness, or lease fencing.

### Kubernetes

Use one shared POSIX namespace visible to `stellad` replicas and Session Pods. Each Session Pod receives only exact authorized mount views. Pod scheduling must not encode durable workspace location.

Required platform checks:

- cross-node namespace visibility and POSIX conformance;
- mount identity/readiness and failure behavior;
- Pod UID/compute-generation fencing and stale exec rejection;
- exact mount isolation and read-only Skill views;
- no ServiceAccount token, hostPath/socket, privileged mode or host namespaces;
- non-root/drop-all/seccomp, narrow RBAC, default-deny egress and resource limits;
- node loss, Pod replacement and continued access to the same workspace bytes;
- reuse of the Compose distributed protocol journeys without scheduling-based data ownership or owner RPC.

Helm must reject `replicaCount > 1` until both prerequisites and Kubernetes conformance are satisfied for the selected storage backend.

## Verification by layer

- Pure typed-root, containment, guard and state-transition logic: Go unit tests.
- PostgreSQL constraints, generation/lease CAS, FIFO and authorization coupling: DB integration tests.
- Real process startup, SSE, abort and recovery: subprocess system tests.
- Independent replicas and process failure: Compose tests.
- Shared filesystem behavior, mount readiness, node failure and Pod security: Kubernetes/storage conformance.

For each implementation change, run focused tests first, then the repository-required `mise run format && mise run build && mise run test`. Run `mise run system-test` where a subprocess seam changes and record supported-host limitations honestly.

## Explicitly rejected or superseded

The following names may describe removed code during audit, but are not active work: `HomeStore`, `HomeAttachment`, `storage_home`, `storage_migration`, Store ID/locator, ready/tombstone, physical purge, `storage/home-physical-purge`, durable-workspace `stella-fs`, exact-Session filesystem RPC, `Runtime.UseFilesystem`, `BeginUse`/`Open`, per-Agent RWO PVC/affinity/trusted provisioner.

Also excluded: a file-access Pod, helper/image revision coupling, a generic mutable-asset migration, S3 as the live workspace interface, transparent unknown-outcome retry, fixed full-program PR counts, and unapproved future branch names. Immutable `BlobStore`/S3 is not removed.

## Superseded review history

Earlier Fable and Sol reviews approved prior revisions involving registry-backed storage, helper transport, physical cleanup, per-Agent Kubernetes volumes, revision-tree Skill publication, and fixed PR maps. Those findings are historical only and do not approve this rewritten architecture or plan. Git history retains their details; active execution follows the sequence and acceptance gates above.
