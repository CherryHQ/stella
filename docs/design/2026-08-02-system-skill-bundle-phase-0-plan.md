# Plan: Phase 0 — Builtin Skill bundle and Agent Skill policy

- **Revision:** 4 — merged Phase 0 record reconciled with the current POSIX workspace plan
- **Status:** COMPLETED — PR #831 merged; this file is an implementation record, not the active downstream plan
- **Issue:** CherryHQ/stella#828 (umbrella; V requested no child Issue)
- **Delivered PR:** CherryHQ/stella#831 (merged into `main`)
- **Parent architecture:** `docs/design/2026-08-01-sandbox-architecture-v2.md`
- **Parent plan:** `docs/design/2026-08-02-sandbox-architecture-v2-implementation-plan.md`
- **Umbrella:** CherryHQ/stella#828

The current parent documents supersede this record's original downstream architecture. The merged builtin bundle and Agent policy remain valid; #862 merged as `d05375f4e28b364a5023cdf6e15ccf4b83f9d378`, #886 is the current rooted-operation implementation, and future mutable Skill work follows #897 in the parent plan. Historical Home, helper-transport, revision-tree, fixed-PR-count, and Kubernetes placement decisions below have no execution authority. Issue #828 is the umbrella tracker; the parent implementation plan is the sole execution source.

## Problem before PR #831

Before PR #831, Stella called two different things “system Skill”:

1. release-owned builtin Skills under `resources/skills/system/` are embedded, rewritten into `$STELLA_HOME/.agents/skills` at startup, scanned back from disk, and host-mounted into sandboxes;
2. administrator/user-installed Skills use PostgreSQL `skill`/`skill_file`, then materialize into derived host directories.

The current Sandbox v2 target has two Skill byte authorities:

- immutable builtin release bundle;
- deterministic POSIX roots for mutable `system`, `system_agent`, `user`, `user_agent`, and `project` content. PostgreSQL retains policy, narrowly justified migration state, and Reflect business telemetry rather than a mutable content mirror.

Phase 0 established the first authority and a storage-independent Agent policy. Mutable PG Skill bytes move only after #886 provides rooted POSIX operations; #897 owns that domain-specific cutover through deterministic `WorkspaceManager` roots.

The pre-#831 builtin handling also had concrete defects:

- startup rewrites roughly 322 files / 2.8 MiB and forces `0644`, losing executable mode;
- `internal/skills.Service` scans the extracted filesystem instead of using the process-wide `resources.Registry`;
- filesystem IDs include host paths;
- filesystem frontmatter parsing drops nested `metadata.owner_plugin` used by plugin visibility;
- Docker mounts a host builtin tree rather than proving image/binary revision identity;
- `$STELLA_HOME/.agents/skills` intentionally preserves non-builtin entries, so removing its scan without classification would silently hide operator-installed system Skills.

V also requires an administrator or an Agent’s durable creator to enable/disable individual builtin, `system`, and matching `system_agent` Skills for that Agent. The existing `agent.enabled_builtin_skills` JSONB column must be reused; no activation table or global hard-lock hierarchy is needed.

## Evidence at Phase 0 planning time

- `resources/{embed,registry,extract,skills_tree}.go` already provides embed and process-wide registry foundations.
- `BundledSkillSpec` and plugin bundle sync already define build-time external Skill ingestion.
- `internal/skills/{service,prompt,tool,fs_project}.go` owns catalog precedence and every model-reachable prompt/search/load path.
- `internal/agent/access/` already defines `Manage = admin OR durable creator`; delegated/group/system actors cannot manage an Agent.
- `agent.enabled_builtin_skills` existed as JSONB but had no runtime reader. Broad Agent create/update adapters wrote `[]`, so neither empty nor non-empty legacy arrays had trustworthy allowlist semantics.
- `PoolManager.InvalidateAgent` already invalidated one Agent’s current-process runner.
- Docker already built restricted tools from the build-mounted `stellad` binary and supported image-provided Stella directories.
- The current parent plan assigns rooted POSIX operations to #886 and the mutable Skill byte cutover to #897; Phase 0 does not duplicate either responsibility.

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

Parent #897 performs any required one-time mutable byte-authority cutover after #886 rooted operations exist. PostgreSQL may retain Skill policy, narrowly scoped migration records, and Reflect business telemetry, but not a mutable Skill content mirror or restore-on-miss path.

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

This is intentionally strict. Creating a throwaway legacy mutable-root protocol would preserve the host coupling Phase 0 is meant to stop.

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

Commit invalidates the affected local runner. An in-flight turn keeps its built snapshot; the next turn reloads. Future distributed lifecycle work must provide cross-replica policy invalidation and decide whether AgentRun persists or pins a policy digest for a whole Run.

### 7. Mutable POSIX authority is deferred to the active parent plan

The current parent order is:

```text
#831 builtin bundle and policy (merged)
→ #862 local WorkspaceManager foundation
→ #886 rooted POSIX operations and Sandbox mount boundary
→ #888 durable file consumers
→ #897 mutable Skill filesystem authority
→ optional #928 residual path cleanup
→ shared-POSIX readiness + distributed lifecycle fencing
→ Compose/Kubernetes conformance
```

#897 must first prove which mutable Skill data requires migration. Any cutover is Skill-specific, offline, and verified, with one content authority before and after. It targets deterministic POSIX roots and does not add a directory catalog. This completed Phase 0 record does not implement or further constrain that cutover.

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
| architecture and parent plan                                         | current builtin-versus-mutable-byte authority and the #862/#886/#888/#897 sequence                                                          |

## Compatibility and rollback

- No Skill schema/table or scope value changes in Phase 0.
- Existing PG Skill rows and behavior remain authority for mutable scopes.
- All legacy policy arrays preserve current all-enabled behavior; non-empty arrays are observable but not reinterpreted.
- Bundle activation blocks rather than losing a custom legacy filesystem system Skill.
- A downgrade binary ignores canonical policy and may overwrite the column on an ordinary Agent update. Release notes therefore require clearing disabled refs before downgrade; Phase 0 activation is product preference, not a security hard-lock.
- Content-addressed bundle directories are derived and may remain inert after rollback.

## Tasks

The checklist below records how merged PR #831 was planned. It is not the live program dashboard and does not authorize the superseded downstream architecture; Issue #828 is the umbrella tracker and the parent implementation plan is the sole execution source.

### Phase 1: Deterministic builtin manifest and Registry

**Why first:** runtime cannot leave the extracted tree until the release catalog independently represents every builtin byte, mode and metadata field.

- [ ] Generate one deterministic manifest from synced nested builtin roots and integrate it into `mise run generate`/drift checks.
- [ ] Extend `resources.Registry` with stable descriptors, complete metadata, file reads, source mode, per-Skill digest and bundle revision.
- [ ] Implement the content-addressed installer with ceilings, canonical path checks, digest/mode verification, temporary publication, winner verification and read-only completion marker.
- [ ] Add the minimum `stellad system-bundle revision|install|verify` build/operator surface needed by image builds; command help remains the syntax source of truth.
- [ ] Test nested roots, executable scripts, duplicate/name mismatch, traversal/symlink rejection, deterministic output, tampering, concurrency and no-op reinstall mtime.

**Acceptance:**

- [ ] `mise run generate && git diff --exit-code -- resources/` proves committed generation is deterministic.
- [ ] Focused resources/command tests exit 0 for manifest, installer, tamper, concurrent publication and source mode cases.
- [ ] A second verified install performs no file rewrite; partial/tampered final trees are never returned.
- [ ] No new dependency, service, table, cache watcher or host-path public contract is introduced.

### Phase 2: Provider bundle cutover and legacy gate

**Why after Phase 1:** bundle readiness and legacy safety must be proven before removing the extracted builtin source.

- [ ] Add nested skill-root inventory against the generated manifest; list every custom/residual blocker and fail the new capability gate without deleting anything.
- [ ] Inject `resources.Registry` into Service/Tool/prompt/API builtin composition; preserve project and PG adapters plus existing precedence.
- [ ] Install the exact revision for local/none; map it read-only to `/opt/stella/skills/builtin` for isolating local, bake/label/verify and expose the same view in Docker, and stop mounting the host builtin tree. Non-isolating Providers retain exact paths through `SkillView`.
- [ ] Fail complete Docker readiness on mismatch with expected/actual revisions and actionable dev/custom-image rebuild instructions.
- [ ] Remove generic startup builtin extraction and filesystem builtin scan only after the legacy gate passes.
- [ ] Correct the touched Dockerfile’s stale registry comment without unrelated cleanup.

**Acceptance:**

- [ ] A nested custom system Skill fixture blocks activation and reports its exact skill root; manifest roots pass and no fixture bytes are changed.
- [ ] Direct builtin list/file reads use Registry/embed without SkillStore calls or runtime mirror writes; merged name resolution still checks PG shadowing once.
- [ ] Local/none/provider tests prove the exact revision path, read-only `/opt/stella/skills/builtin` view where isolation exists, and executable helpers; Docker tests prove no host builtin mount and fail-closed revision mismatch.
- [ ] `mise run sandbox:docker:build` and focused provider contract tests pass on a supported trusted runner.

### Phase 3: Existing-column Agent Skill policy

**Why after Phase 2:** stable builtin refs must exist before an Agent policy can safely name them.

- [ ] Add strict versioned `AgentSkillPolicy` parsing/canonicalization and logical-ref validation. Treat every legacy array as no disables; diagnose only non-empty arrays.
- [ ] Remove `enabled_builtin_skills` from broad Agent update SQL/adapters; create/seed uses the existing default. Add a dedicated row-locked policy read/mutate query/service.
- [ ] Add spec-first contextual Agent Skill enabled state and exact logical-ref activation endpoint; regenerate Go/TypeScript clients before server/Web changes.
- [ ] Authorize policy writes through `agentAccess.Manage`; retain existing Skill content PEP and test owner-toggle/admin-content separation.
- [ ] Apply one effective policy filter after precedence to prompt/search/load/file/helper and all future Skills-tool read actions. Keep disabled entries visible in management views.
- [ ] Invalidate the affected local runner only after commit; document in-flight-turn/next-turn behavior and parent multi-replica policy-digest handoff.
- [ ] Update Agent Skills UI with real activation switches, enabled filter, dangling-ref diagnostic and separate content controls.

**Acceptance:**

- [ ] Changing every ordinary Agent field preserves canonical policy bytes; two concurrent policy mutations serialize without losing either committed logical ref.
- [ ] Empty/non-empty legacy arrays keep all applicable Skills enabled; non-empty diagnostics appear; first explicit write produces canonical v1 JSON without inferred disables.
- [ ] Admin/creator/other/delegated/group/system authorization matrix passes; owner toggles system content but cannot edit/delete it.
- [ ] Disabled winner never falls back; exact ID/name/file/helper paths cannot bypass; policy read/decode errors fail closed; `disable_model_invocation` remains independent.
- [ ] Current turn keeps its snapshot and the next turn observes commit after runner invalidation.
- [ ] OpenAPI generation, focused backend tests and `cd web && vp test run` exit 0.

### Phase 4: Architecture reconciliation and PR gate

**Why last:** final docs must describe the proven Phase 0 behavior and the approved later authority cutover without claiming it already landed.

- [ ] Keep the current parent architecture and plan synchronized with implementation discoveries; preserve the builtin-versus-mutable-byte authority boundary without prescribing a Home catalog, filesystem RPC, or Kubernetes placement model.
- [ ] Update README and EN/ZH user/developer Skill/storage/sandbox docs plus builtin Stella Skill for builtin/system distinction, activation semantics, custom-image rebuild, legacy gate and downgrade behavior.
- [ ] Run adversarial bug/security/architecture/correctness review; resolve findings affecting legacy data, policy clobber/bypass, bundle integrity, provider isolation or the future mutable POSIX Skill cutover.
- [ ] Run mandatory local checks and the lowest sufficient startup/image system seam.
- [ ] Commit each phase, push the stacked branch, and open one Draft PR based on `design/sandbox-architecture-v2` whose body says `Refs #828`. Do not create a child Issue or mark the PR ready without V's instruction.

**Acceptance:**

- [ ] `mise run format && mise run build && mise run test` exits 0.
- [ ] `mise run test` exits 0 or records the project-defined unsupported-host skip.
- [ ] `git diff --check`, generated-code drift checks and focused dprint checks exit 0.
- [ ] Fresh startup performs no builtin rewrite; custom legacy roots block safely; Docker uses only its matching image bundle; Agent policy survives restart and ordinary Agent updates.
- [ ] `gh pr view <new-pr> --json baseRefName,headRefName,isDraft,body` shows the correct stack, linked Issue, Fable-approved decisions and verification.

## Review log

The reviews below are historical evidence for the merged Phase 0 implementation. Their approval of then-proposed Home, helper transport, revision-tree, or fixed-stack details does not approve or override the current parent architecture and plan.

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

**Historical review: APPROVED.** No unresolved mandatory finding for revision 3. Its approval of the then-proposed Homes/helper/offline-cutover order is superseded; only the merged Phase 0 bundle and policy result remains current.

### Revision 3 exact-plan transcription review

**Review (Fable, round 1): CHANGES REQUIRED.** The parent Phase 2 no-GC ceiling lacked an observable disk-growth task and runnable acceptance.

**Historical resolution:** The revision 3 parent plan then added OTel retained-revision metrics, threshold warnings, capacity-response docs, and a fixture. That revision-tree requirement is superseded; #897 must justify any revision protocol independently.

**Review (Fable, round 2): APPROVED.** Fable re-read architecture rev 8, the parent plan, and this byte-identical exact plan, found no mandatory issue, and confirmed the `/opt/stella/skills/builtin` Phase 0 view remains a projection of the immutable bundle rather than another authority.

**Historical resolution:** The revision 3 plan gate was satisfied. Current sequencing follows the parent implementation plan; Issue #828 remains the umbrella tracker.

### Full-program execution map

**Decision (V):** Implement all parent phases under umbrella Issue #828, create no child Issues, and open every PR as Draft. The initial 23-PR decomposition was too granular; V authorized Sol self-review instead of another Fable pass.

**Historical resolution:** the old parent plan used 15 vertical Draft PRs. That fixed map is superseded. PR #831 subsequently merged, and the current parent plan intentionally has no fixed full-program PR count.

## Handoffs

<!-- Mason appends one concrete handoff after each completed phase. -->
