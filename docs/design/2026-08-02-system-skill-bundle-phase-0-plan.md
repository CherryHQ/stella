# Plan: Phase 0 — Builtin Skill bundle and Agent Skill policy

- **Revision:** 5 — final gate hardening and acceptance
- **Status:** IMPLEMENTED — Phase 0 acceptance gates complete; Draft PR #831 remains Draft
- **Issue:** CherryHQ/stella#828 (umbrella; V requested no child Issue)
- **Branch:** `refactor/system-skill-bundle`
- **PR base:** `design/sandbox-architecture-v2` (stacked above Draft PR #829; retarget to `main` after #829 merges)
- **Parent architecture:** `docs/design/2026-08-01-sandbox-architecture-v2.md` rev 8, D1–D60
- **Parent plan:** `docs/design/2026-08-02-sandbox-architecture-v2-implementation-plan.md`
- **Umbrella:** CherryHQ/stella#828

## Problem

Stella currently calls two different things “system Skill”:

1. release-owned builtin Skills under `resources/skills/system/` are embedded, rewritten into `$STELLA_HOME/.agents/skills` at startup, scanned back from disk, and host-mounted into sandboxes;
2. administrator/user-installed Skills use PostgreSQL `skill`/`skill_file`, then materialize into derived host directories.

The complete Sandbox v2 target has only two Skill current-state authorities:

- immutable builtin release bundle;
- mutable Home filesystem roots for `system`, `system_agent`, `user`, `user_agent`, and `project`.

Phase 0 can safely establish the first authority and a storage-independent Agent policy now. It cannot move mutable PG Skills before typed Homes and provider-native `stella-fs` exist; doing so would create another host-coupled directory that Phase 2 immediately deletes.

Current builtin handling also has concrete defects:

- startup rewrites roughly 322 files / 2.8 MiB and forces `0644`, losing executable mode;
- `internal/skills.Service` scans the extracted filesystem instead of using the process-wide `resources.Registry`;
- filesystem IDs include host paths;
- filesystem frontmatter parsing drops nested `metadata.owner_plugin` used by plugin visibility;
- Docker mounts a host builtin tree rather than proving image/binary revision identity;
- `$STELLA_HOME/.agents/skills` intentionally preserves non-builtin entries, so removing its scan without classification would silently hide operator-installed system Skills.

V also requires an administrator or an Agent’s durable creator to enable/disable individual builtin, `system`, and matching `system_agent` Skills for that Agent. The existing `agent.enabled_builtin_skills` JSONB column must be reused; no activation table or global hard-lock hierarchy is needed.

## Evidence and existing mechanisms

- `resources/{embed,registry,extract,skills_tree}.go` already provides embed and process-wide registry foundations.
- `BundledSkillSpec` and plugin bundle sync already define build-time external Skill ingestion.
- `internal/skills/{service,prompt,tool,fs_project}.go` owns catalog precedence and every model-reachable prompt/search/load path.
- `internal/agent/access/` already defines `Manage = admin OR durable creator`; delegated/group/system actors cannot manage an Agent.
- `agent.enabled_builtin_skills` exists as JSONB but has no runtime reader. Current broad Agent create/update adapters write `[]`, so neither empty nor non-empty legacy arrays have trustworthy current allowlist semantics.
- `PoolManager.InvalidateAgent` already invalidates one Agent’s current-process runner.
- Docker already builds restricted tools from the build-mounted `stellad` binary and supports image-provided Stella directories.
- Parent rev 8 owns the later SystemSkillRoot/SystemAgentSkillRoot and PG→Home cutover; Phase 0 must not duplicate them.

## Design decisions

### 1. Phase 0 changes builtin authority, not mutable Skill authority

During Phase 0 the transitional catalog sources are explicit:

```text
builtin       resources.Registry + immutable bundle
system/...    existing PostgreSQL SkillStore
project       existing project filesystem
```

There is no synchronization between them. Existing precedence remains:

```text
project > user_agent > user > system_agent > system > builtin
```

`system` and `system_agent` keep their current wire names and PG lifecycle. Phase 0 does not migrate/drop `skill`, `skill_file`, `skill_usage`, or `skill_changelog`, and it does not add a temporary PG→host snapshot protocol.

Parent Phase 2, after Homes + `stella-fs`, performs the one-time mutable authority cutover described by D59/D60. Parent Phase 3 no longer implements PG `LoadSnapshot` or DB Skill scratch.

### 2. One deterministic manifest and bundle own builtins

Generation walks synced `resources/skills/system` at the same nested skill-root granularity as the current resource scanner. Each manifest entry contains:

- stable `builtin:<name>` logical ref and `builtin-<name>` API ID;
- name, description, `disable_model_invocation`, complete metadata including `owner_plugin`;
- canonical relative root/files, SHA-256, sizes, and allowed source mode (`0644` or `0755`);
- per-Skill digest and one ordered bundle revision.

Generation rejects duplicate names, mismatched root/frontmatter names, non-canonical paths, symlinks, unsupported modes, missing `SKILL.md`, and size/count ceilings. The embedded files remain the content source; the manifest supplies stable identity, metadata and mode.

`resources.Registry` becomes the only builtin catalog/text reader. Runtime never treats an extracted directory as builtin authority.

### 3. Execution projection is content-addressed and Provider-verified

- local/none installs the embedded tree once under `$STELLA_HOME/bundles/<revision>` through a temporary sibling, full verification and atomic final publication;
- Linux local/bwrap maps that exact revision read-only to `/opt/stella/skills/builtin`; none/macOS-local uses the exact host path through `SkillView`;
- Docker bakes the same revision into `/opt/stella/bundles/<revision>`, exposes it read-only at `/opt/stella/skills/builtin`, records it in image metadata/marker, and treats it as image-provided;
- binary/image revision mismatch fails complete Docker readiness with expected/actual values and `mise run sandbox:docker:build`; it never falls back to a host mirror;
- Kubernetes later consumes the same image contract.

The installer preserves executable helpers, performs no write on an already verified revision, and never exposes a partial final tree. Concurrent installers accept another winner only after verifying its complete marker/content.

### 4. Legacy filesystem system Skills fail closed before scan removal

Current extraction preserves non-builtin entries. Phase 0 therefore inventories `$STELLA_HOME/.agents/skills` using the same nested **skill-root** algorithm as `listSkillRoots`, not top-level directory names.

- exact manifest-owned roots are derived builtin projection;
- every non-manifest Skill root or unrecognized residual path is a blocker;
- the capability gate lists all blockers and refuses to activate the new bundle behavior;
- nothing is deleted or silently hidden;
- the operator stays on the old binary or imports those entries through the current managed system path before retrying.

This is intentionally strict. Creating a throwaway “legacy custom root” protocol before HomeStore would preserve the host coupling Phase 0 is meant to stop.

### 5. Reuse the Agent JSONB column as a versioned policy

The physical column remains `agent.enabled_builtin_skills`. Domain/API code calls it `AgentSkillPolicy` and writes canonical JSON:

```json
{
  "version": 1,
  "disabled": [
    "builtin:code-review",
    "system:company-style",
    "system_agent:deploy"
  ]
}
```

Rules:

- missing, `null`, empty array and non-empty legacy array all preserve current “no disabled refs” behavior;
- a non-empty legacy array emits an admin-visible diagnostic but never infers an allowlist/complement;
- first explicit policy mutation writes canonical v1 JSON;
- only `builtin:<name>`, `system:<name>`, and `system_agent:<name>` for the target Agent are valid;
- scope+name is persistent identity; rename is unsupported and means delete+create;
- dangling refs are ignored by execution, shown in management diagnostics, and removed only explicitly.

A dedicated transaction locks the Agent row, decodes/mutates one logical ref, and updates only this column. In the same PR, broad `UpdateAgent` SQL/adapters stop writing the column; create/seed rely on the DB default. This prevents a normal name/model/prompt edit from silently re-enabling Skills.

No new table or global admin policy layer is added. Admin and creator mutate the same Agent setting; commit order decides the final value.

### 6. Activation is applied after precedence and at every read path

The service resolves the effective winner first, then applies the Agent policy. Disabling a shadowing `system:x` makes logical `x` unavailable; it does not reveal `builtin:x`.

One policy snapshot applies to:

- prompt catalog;
- `search_installed` and any future Skills-tool read action;
- name and stable-ID load;
- reference/file reads;
- returned helper `skill_dir`.

Direct IDs cannot bypass a disabled or shadowed winner. `disable_model_invocation` remains separate from activation. Policy decode/read failure fails closed; there is no default-enabled fallback after an actual error.

The activation API starts in OpenAPI and uses exact logical refs under the Agent route. Writes authorize with `agentAccess.Manage`; content edit/delete remains under existing Skill policy, so an owner may toggle admin content but cannot mutate it.

Commit refreshes and invalidates the affected local runner behind a per-Agent admission barrier. Admission atomically reserves the selected runner and captures runner/model/thinking in one immutable lease; every non-terminal reset preserves admitted or busy work and marks it stale for replacement. An in-flight turn keeps that snapshot; the next turn reloads. Terminal shutdown and Session removal remain explicit eager-close paths.

A PostgreSQL `COMMIT` error is outcome unknown and is never retried. Before another local turn can admit, Stella reloads durable Agent policy under the same barrier. If that refresh cannot prove a safe current snapshot, the runtime installs a rejecting factory and invalidates old runners so subsequent admission fails closed. Parent Phase 4 later adds policy digest `NOTIFY`; parent AgentRun persists/pins the digest for a whole Run.

### 7. Mutable Home authority is a hard deferred dependency, not a Phase 0 cache

The approved parent order is:

```text
Phase 0 builtin/policy
→ Phase 1 typed Homes + shared Skill roots
→ Phase 2a stella-fs/publication
→ Phase 2b offline PG Skill/Reflect cutover
→ Phase 3 AgentRun catalog/policy pin + revision GC
→ Phase 4 Compose multi-replica
→ Phase 5 Kubernetes
```

Phase 2b migrates active rows into exact Home roots, deprecated/changelog state into an archive, and Reflect to filesystem digest identity plus logical usage telemetry. Marker before/after each has one authority. This plan changes the parent documents but does not implement that cutover.

## Alternatives rejected

- **Keep builtin in PG:** release artifacts become mutable business rows and still need executable projection.
- **Keep PG as final mutable authority:** preserves a DB/disk mirror and a disposable snapshot mechanism incompatible with ordinary POSIX edits.
- **Move PG Skills now:** no typed Home or provider-native filesystem exists yet; the destination would be another host path.
- **ConfigMap/initContainer/sidecar/FUSE/remote bundle service:** duplicates image/Home lifecycle and adds availability/authentication surfaces.
- **Reuse legacy array as allowlist:** repository behavior cannot prove that meaning and current Agent writes have already clobbered values.
- **Add `agent_skill_override`:** one versioned Agent column and existing Agent PEP are sufficient.
- **Disable before precedence:** unexpectedly reveals lower same-name implementations.

## What changes where

| Area                                                                 | Phase 0 change                                                                                                                              |
| -------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------- |
| `resources/`                                                         | deterministic manifest generator, full builtin descriptors/files/modes/digests, bundle revision and verified installer                      |
| plugin bundle sync                                                   | run manifest generation after `BundledSkillSpec` synchronization and detect drift                                                           |
| `internal/skills/`                                                   | Registry-backed builtin adapter, stable builtin identity, unchanged PG/project adapters, one effective-policy filter across every read path |
| `internal/config`, `internal/store`, `internal/db/queries/agent.sql` | typed policy codec, dedicated row-locked mutation, broad Agent update no longer writes legacy column                                        |
| `internal/agent`, `internal/server`                                  | Agent Manage authorization, runner invalidation, contextual list/get and exact-ref activation endpoint                                      |
| `api/spec/domain/agents/`, generated SDK                             | readable builtin scope, contextual enabled state, exact logical-ref policy mutation                                                         |
| `web/src/features/agents/`                                           | true activation switch/filter separate from `disable_model_invocation` and content permissions                                              |
| local/none/Docker providers                                          | content-addressed bundle placement, current-revision view, image marker/readiness, removal of builtin host mount                            |
| architecture and parent plan                                         | rev 8 two-authority target and the Homes/stella-fs/offline-cutover order                                                                    |

## Compatibility and rollback

- No Skill schema/table or scope value changes in Phase 0.
- Existing PG Skill rows and behavior remain authority for mutable scopes.
- All legacy policy arrays preserve current all-enabled behavior; non-empty arrays are observable but not reinterpreted.
- Bundle activation blocks rather than losing a custom legacy filesystem system Skill.
- A downgrade binary ignores canonical policy and may overwrite the column on an ordinary Agent update. Release notes therefore require clearing disabled refs before downgrade; Phase 0 activation is product preference, not a security hard-lock.
- Content-addressed bundle directories are derived and may remain inert after rollback.

## Tasks

### Phase 1: Deterministic builtin manifest and Registry

**Why first:** runtime cannot leave the extracted tree until the release catalog independently represents every builtin byte, mode and metadata field.

- [x] Generate one deterministic manifest from synced nested builtin roots and integrate it into `mise run generate`/drift checks.
- [x] Extend `resources.Registry` with stable descriptors, complete metadata, file reads, source mode, per-Skill digest and bundle revision.
- [x] Implement the content-addressed installer with ceilings, canonical path checks, digest/mode verification, temporary publication, winner verification and read-only completion marker.
- [x] Add the minimum `stellad system-bundle revision|install|verify` build/operator surface needed by image builds; command help remains the syntax source of truth.
- [x] Test nested roots, executable scripts, duplicate/name mismatch, traversal/symlink rejection, deterministic output, tampering, concurrency and no-op reinstall mtime.

**Acceptance:**

- [x] `mise run generate && git diff --exit-code -- resources/` proves committed generation is deterministic.
- [x] Focused resources/command tests exit 0 for manifest, installer, tamper, concurrent publication and source mode cases.
- [x] A second verified install performs no file rewrite; partial/tampered final trees are never returned.
- [x] No new dependency, service, table, cache watcher or host-path public contract is introduced.

### Phase 2: Provider bundle cutover and legacy gate

**Why after Phase 1:** bundle readiness and legacy safety must be proven before removing the extracted builtin source.

- [x] Add nested skill-root inventory against the generated manifest; list every custom/residual blocker and fail the new capability gate without deleting anything.
- [x] Inject `resources.Registry` into Service/Tool/prompt/API builtin composition; preserve project and PG adapters plus existing precedence.
- [x] Install the exact revision for local/none; map it read-only to `/opt/stella/skills/builtin` for isolating local, bake/label/verify and expose the same view in Docker, and stop mounting the host builtin tree. Non-isolating Providers retain exact paths through `SkillView`.
- [x] Fail Docker Provider preflight on mismatch with expected/actual revisions and actionable dev/custom-image rebuild instructions.
- [x] Remove generic startup builtin extraction and filesystem builtin scan only after the legacy gate passes.
- [x] Correct the touched Dockerfile’s stale registry comment without unrelated cleanup.

**Acceptance:**

- [x] A nested custom system Skill fixture blocks activation and reports its exact skill root; manifest roots pass and no fixture bytes are changed.
- [x] Direct builtin list/file reads use Registry/embed without SkillStore calls or runtime mirror writes; merged name resolution still checks PG shadowing once.
- [x] Local/none/provider tests prove the exact revision path, read-only `/opt/stella/skills/builtin` view where isolation exists, and executable helpers; Docker tests prove no host builtin mount and fail-closed revision mismatch.
- [x] `mise run sandbox:docker:build` and focused provider contract tests pass on a supported trusted runner.

### Phase 3: Existing-column Agent Skill policy

**Why after Phase 2:** stable builtin refs must exist before an Agent policy can safely name them.

- [x] Add strict versioned `AgentSkillPolicy` parsing/canonicalization and logical-ref validation. Treat every legacy array as no disables; diagnose only non-empty arrays.
- [x] Remove `enabled_builtin_skills` from broad Agent update SQL/adapters; create/seed uses the existing default. Add a dedicated row-locked policy read/mutate query/service.
- [x] Add spec-first contextual Agent Skill enabled state and exact logical-ref activation endpoint; regenerate Go/TypeScript clients before server/Web changes.
- [x] Authorize policy writes through `agentAccess.Manage`; retain existing Skill content PEP and test owner-toggle/admin-content separation.
- [x] Apply one effective policy filter after precedence to prompt/search/load/file/helper and all future Skills-tool read actions. Keep disabled entries visible in management views.
- [x] Invalidate the affected local runner only after commit; document in-flight-turn/next-turn behavior and parent multi-replica policy-digest handoff.
- [x] Update Agent Skills UI with real activation switches, enabled filter, dangling-ref diagnostic and separate content controls.

**Acceptance:**

- [x] Changing every ordinary Agent field preserves canonical policy bytes; two concurrent policy mutations serialize without losing either committed logical ref.
- [x] Empty/non-empty legacy arrays keep all applicable Skills enabled; non-empty diagnostics appear; first explicit write produces canonical v1 JSON without inferred disables.
- [x] Admin/creator/other/delegated/group/system authorization matrix passes; owner toggles system content but cannot edit/delete it.
- [x] Disabled winner never falls back; exact ID/name/file/helper paths cannot bypass; policy read/decode errors fail closed; `disable_model_invocation` remains independent.
- [x] Current turn keeps its snapshot and the next turn observes commit after runner invalidation.
- [x] OpenAPI generation, focused backend tests and `cd web && vp test run` exit 0.

### Phase 4: Architecture reconciliation and PR gate

**Why last:** final docs must describe the proven Phase 0 behavior and the approved later authority cutover without claiming it already landed.

- [x] Keep architecture rev 8 and the parent plan synchronized with implementation discoveries; preserve Phase 2b PG→Home/Reflect marker and Docker-before-Kubernetes gates.
- [x] Update README and EN/ZH user/developer Skill/storage/sandbox docs plus builtin Stella Skill for builtin/system distinction, activation semantics, custom-image rebuild, legacy gate and downgrade behavior.
- [x] Run adversarial bug/security/architecture/correctness review; resolve findings affecting legacy data, policy clobber/bypass, bundle integrity, provider isolation or future Home cutover.
- [x] Run mandatory local checks and the lowest sufficient startup/image system seam.
- [x] Commit each phase, push the stacked branch, and open one Draft PR based on `design/sandbox-architecture-v2` whose body says `Refs #828`. Do not create a child Issue or mark the PR ready without V's instruction.

**Acceptance:**

- [x] `mise run format && mise run build && mise run test` exits 0.
- [x] `mise run system-test` exits 0 or records the project-defined unsupported-host skip.
- [x] `git diff --check`, generated-code drift checks and focused dprint checks exit 0.
- [x] Startup never rewrites the legacy builtin tree; custom legacy roots block before any Home mutation; Docker uses only its matching read-only image bundle; Agent policy survives restart and ordinary Agent updates.
- [x] `gh pr view <new-pr> --json baseRefName,headRefName,isDraft,body` shows the correct stack, linked Issue, Fable-approved decisions and verification.

## Review log

### Revision 1 — superseded

Proposed `system → tenant`; Fable approved after three corrections. V rejected the scope rename.

### Revision 2 — superseded

Kept system scopes but added `agent_skill_disablement`. Fable approved after stale-row/path corrections. V rejected the new table and required the source/authority model first.

### Revision 3 architecture review — Fable round 1

**Review: CHANGES REQUIRED.**

1. Broad Agent update would erase the reused policy column.
2. Legacy non-empty array allowlist semantics lacked evidence.
3. Reflect ownership/version/usage had no filesystem cutover.
4. Rename-over-nonempty-directory was not a portable atomic update.
5. Legacy custom filesystem system Skills could disappear in Phase 0.
6. Deprecated and PG-only metadata had no finite export disposition.

**Resolved:** Broad updates stop writing policy; all arrays preserve current behavior; Reflect uses filesystem digest identity plus logical usage telemetry; managed writes use contained revision symlink flip; legacy roots fail closed at nested skill-root granularity; metadata/status/changelog have exhaustive active/archive dispositions.

### Revision 3 architecture review — Fable round 2

**Review: APPROVED.** No unresolved mandatory finding. Fable approved the two-authority model, this Phase 0 scope, the later Homes/stella-fs/offline-cutover order, and the required acceptance gates.

### Revision 3 exact-plan transcription review

**Review (Fable, round 1): CHANGES REQUIRED.** The parent Phase 2 no-GC ceiling lacked an observable disk-growth task and runnable acceptance.

**Resolved:** The parent plan now uses the existing OTel pipeline for aggregate retained revision count/bytes/oldest-age, root-threshold structured warnings without high-cardinality labels, capacity-response docs, and a known-size fixture. It also hardens policy-digest and Reflect-marker wording.

**Review (Fable, round 2): APPROVED.** Fable re-read architecture rev 8, the parent plan, and this byte-identical exact plan, found no mandatory issue, and confirmed the `/opt/stella/skills/builtin` Phase 0 view remains a projection of the immutable bundle rather than another authority.

**Resolved:** Final plan gate satisfied. Implementation may start with Phase 1 of this standalone Phase 0 plan.

### Full-program execution map

**Decision (V):** Implement all parent phases under umbrella Issue #828, create no child Issues, and open every PR as Draft. The initial 23-PR decomposition was too granular; V authorized Sol self-review instead of another Fable pass.

**Resolved:** The parent plan now uses 15 vertical Draft PRs. Phase 0 remains one PR with four phase commits, stacked on #829 and retargeted to `main` after #829 merges. Its body uses `Refs #828`, never `Closes #828`; it stays Draft until V says otherwise.

### Revision 4 implementation reconciliation — Sol

**Review: APPROVED FOR FINAL GATE.** Phase 3 exposed two local linearization requirements that the Revision 3 behavior contract implied but did not spell out: admission must reserve its runner and visible metadata atomically, and a PostgreSQL commit error must not reopen admission on an unproved policy snapshot.

**Resolved:** The as-built design uses one per-Agent admission barrier, an immutable `runnerSelection`, reservation-aware non-terminal reset/invalidation, and fail-closed refresh after commit-outcome-unknown. These are Phase 0 current-process mechanisms, not new durable authorities. Architecture rev 8 and the parent plan therefore need no dependency or phase-order change: AgentRun still owns the future whole-Run digest pin, Compose remains the multi-replica gate, and Kubernetes remains blocked behind Compose.

### Revision 5 final gate review — Sol and independent Terra

**Review: APPROVED.** The aggregate four-lens review and executable Docker image gate found two final contract violations: blocked legacy startup changed `bin/` before inventory, and Docker shell execution could mutate the builtin image projection.

**Resolved:** Registry inventory now runs before every Stella Home mutation and locks the ordering with a retired-binary sentinel test. Docker sandbox containers use the platform's read-only root filesystem while keeping only the explicit workspace, principal-data and temporary mounts writable; pure wiring, real-daemon contract and exact-image checks prove the boundary. The independent final reviewer returned `APPROVED` after both corrections. These changes enforce existing Phase 0 contracts and do not alter parent dependencies or authority order.

## Handoffs

- Phase 1: deterministic Registry/bundle committed as `c208ccc0` after generation, installer, mode, tamper and cross-platform gates.
- Phase 2: Provider projection and legacy gate committed as `44c84c05` after local/none/Docker contract and dual-review approval.
- Phase 3: Agent policy/API/UI and admission safety committed as `98d0c829` after security/correctness approval and full local gates.
- Phase 4: EN/ZH docs, plan reconciliation, legacy zero-mutation ordering, Docker read-only rootfs, aggregate review and full local/system/image gates are complete; Draft PR #831 remains Draft.
