---
title: Storage & Durability
---

Everything Stella writes to disk lives under `$STELLA_HOME` (default `~/.stella`, override with `STELLA_HOME`). On a single host with a persistent disk you never have to think about this page. On Kubernetes, on a multi-replica deployment, or anywhere the pod's disk is ephemeral, you do: each directory is either **durable data** you must keep, **derived cache** that rebuilds itself, or **scratch** that is safe to lose.

This page classifies every directory and tells you the volume and backup treatment each one needs. For the environment variables referenced below (`STELLA_DATABASE_URL`, `STELLA_BLOB_S3_*`), see the [environment-variable table on the Deployment page](/docs/start-here/deployment#environment-variables).

## Classification at a glance

| Path under `$STELLA_HOME`                                                             | Holds                                                 | Classification | Kubernetes / ephemeral-disk treatment                                                  |
| ------------------------------------------------------------------------------------- | ----------------------------------------------------- | -------------- | -------------------------------------------------------------------------------------- |
| `postgres/`                                                                           | Embedded PostgreSQL cluster — the source of truth     | Durable        | Persistent volume **and** back it up. Absent when you set `STELLA_DATABASE_URL`.       |
| `users/{id}/data/`                                                                    | User Principal Home: user data and uploads            | Durable        | Persistent POSIX storage; one replica uses local `$STELLA_HOME`.                       |
| `users/group-{id}/data/`                                                              | Group Principal Home: group data and uploads          | Durable        | Same as per-user Principal data.                                                       |
| `users/{principal}/agents/{id}/`                                                      | Per-principal Agent Home: workspace and project files | Durable        | Persistent volume **and** pin to a single replica. Not mirrored anywhere.              |
| `library/`                                                                            | Legacy article mirror (being drained into PostgreSQL) | Legacy         | Keep on a volume until the backfill reports zero missing, then archive or delete.      |
| `bundles/{revision}/`                                                                 | Exact release-provided builtin Skill bundle           | Derived cache  | Reinstalled from the matching binary; do not modify it.                                |
| `.agents/skills/`                                                                     | Legacy Skill inventory                                | Migration gate | Preserve custom roots until they are imported or safely removed.                       |
| `.agents/db-skills/`, `agents/{agent-id}/.agents/skills/`                             | Mutable system and system-Agent Skill authority       | Durable        | Persistent POSIX storage **and** backup; never expose the complete roots to a sandbox. |
| `users/{user-id}/.agents/skills/`, `users/{user-id}/.agents/agent-skills/{agent-id}/` | Mutable user and user-Agent Skill authority           | Durable        | Persistent POSIX storage **and** backup; these bytes are not mirrored to PostgreSQL.   |
| `bin/`                                                                                | Embedded tools and the `stella` CLI                   | Derived cache  | Ephemeral disk is fine. Re-extracted at startup.                                       |
| `.mise-tools/`, `users/{id}/.mise-tools/`                                             | Toolchains for the sandbox                            | Derived cache  | Ephemeral disk is fine. Re-installed on demand.                                        |
| `pg-runtime/`                                                                         | Downloaded and extracted embedded-PostgreSQL runtime  | Derived cache  | Ephemeral disk is fine. Re-download with `stellad postgres download`.                  |
| `users/{id}/data/.cache/`                                                             | Per-user tool cache                                   | Derived cache  | Ephemeral disk is fine.                                                                |
| `cache/sandbox-tmp/`                                                                  | Docker sandbox temporary directories                  | Scratch        | Ephemeral disk is fine; stale directories are removed at startup.                      |
| `runner-scratch/runner-*`                                                             | Disposable workspace for user-less runs               | Scratch        | Never Home authority; clean leftovers only while Stella is stopped or fenced.          |
| `dumps/`                                                                              | Diagnostic dumps written on signal                    | Scratch        | Ephemeral disk is fine. Diagnostic only.                                               |

## PostgreSQL is the source of truth (durable)

PostgreSQL holds nearly all state: configuration, secrets metadata, message history and summaries, Skill identity and policy records, Recally articles and their bodies, the fetched-models cache, goals, schedules, and the scheduler queue. Mutable Skill current-state metadata and file bytes are an exception: they live in typed Home roots. Preserve PostgreSQL together with durable Home and project Skill data; none can be reconstructed from the others.

Phase 1 also records typed Home identity and lifecycle metadata in PostgreSQL: user and group Principal Homes, per-principal Agent Homes, and the narrow system and system-Agent Skill roots. That stable metadata does **not** make Home file bytes recoverable. Back up PostgreSQL together with every durable Principal Home and Agent Home storage location.

- **Embedded cluster (default):** the data lives in `$STELLA_HOME/postgres/`. This directory must sit on a persistent volume and be backed up (stop the server first, or use a filesystem snapshot). The downloaded runtime under `pg-runtime/` is just code and can be re-fetched.
- **External server (`STELLA_DATABASE_URL`):** the database moves out of `$STELLA_HOME` entirely. Back it up with `pg_dump` against your database. This is the recommended setup for Kubernetes — it takes the single hardest-to-manage stateful directory off the pod.

## User assets (durable POSIX data)

Files users upload are written to `users/{id}/data/assets/` (and `users/group-{id}/data/assets/` for groups). This live mutable tree is part of the Principal Home and has the same durability requirements. Workspace APIs, channel attachment ingestion, and Agent mounts observe the same POSIX bytes.

S3 is not a mirror or recovery authority for this mutable tree. The legacy mutable-asset S3 authority was not deployed in the supported upgrade population, so no mutable-asset migration or marker is required. Keep and back up persistent POSIX storage. `STELLA_BLOB_S3_*` remains available for separate immutable BlobStore responsibilities such as content-addressed session media; it does not make Principal Home recoverable.

## Principal and Agent Homes (durable, single-replica)

The current local store uses `users/{id}/data/` and `users/group-{id}/data/` as user and group Principal Homes; `users/{principal}/agents/{id}/` is each Principal's Agent Home. An Agent Home holds that principal's mutable working tree and project files. Nothing mirrors these mutable bytes to PostgreSQL or S3. They are durable data: use persistent POSIX storage. The current product supports one replica.

These deterministic paths are the storage layout for the current single-replica POSIX product. PostgreSQL owner rows authorize access, while the filesystem retains the bytes. Future replicas must mount one globally shared, strongly consistent POSIX namespace with the same deterministic layout; S3 is not a replacement for it.

Stella supports one replica and one POSIX `STELLA_HOME`. PostgreSQL user, group, Agent, and assignment rows remain identity and authorization authority; there is no PostgreSQL directory catalog. Deterministic roots are `users/<user-id>`, `users/group-<group-id>`, their nested `agents/<agent-id>`, and global `agents/<agent-id>`. The filesystem is layout and data authority. A missing root for a live owner is created with its internal scaffold. Symbolic links, non-directories, and unsafe IDs are rejected. The host is trusted and must preserve normal POSIX semantics.

## Destructive owner deletion

An explicit destructive owner deletion takes the process lifecycle fence, then the local owner gate, then deletes the owner in its existing database transaction. Files and inodes are retained. After commit, owner existence checks reject new workspace views and admission. Removing an assignment, removing a group member, archiving a Session, and uninstalling Helm do not delete workspace bytes.

Within one server process, a writer-prioritized admission barrier prevents runner setup from racing destructive deletion. The barrier is released after synchronous runner selection and Home resolution; it does not wait for an active turn to finish. This is a single-replica guarantee, not a distributed lease. Future multi-replica support requires PostgreSQL-backed generations or leases in addition to each process's local barrier. A failed best-effort runtime refresh after a durable management change can remain stale until a later reconciliation.

An orphaned global `agents/<agent-id>` entry reserves that Agent ID; any file, directory, or symbolic link counts. Trusted-host manual removal permits reuse. Multi-replica storage, S3 data authority, generations, and distributed leases require a future redesign rather than extending this local contract.

## Legacy article mirror (draining)

`library/` is a leftover from when Recally article bodies were stored as files on disk. Bodies now live in PostgreSQL, and the only thing that still reads these files is a startup job that backfills any file-only bodies into the database — article reads serve exclusively from PostgreSQL and never fall back to disk. Nothing writes new files here. Keep the directory on a volume until a backfill run logs zero missing bodies; after that the files are inert legacy data and are safe to archive or delete.

## Skills

Builtin Skills are the exact release bundle at `bundles/{revision}/`. Native `local` and `none` execution installs that bundle; isolating execution reads it at `/opt/stella/skills/builtin`. The `/opt` path is an execution coordinate, not a second content authority.

Project Skills remain ordinary files in durable Agent/project working trees. Mutable `system`, `system_agent`, `user`, and `user_agent` Skills use immutable digest revisions in these typed Home roots:

- `system`: `.agents/db-skills/`
- `system_agent`: `agents/{agent-id}/.agents/skills/`
- `user`: `users/{user-id}/.agents/skills/`
- `user_agent`: `users/{user-id}/.agents/agent-skills/{agent-id}/`

Each logical Skill has an atomic current selector. PostgreSQL retains identity, ownership, policy, usage, provenance, and migration evidence, but not mutable current-state bytes after cutover. Back up both PostgreSQL and all four typed Home roots.

The model does not receive these authority roots. After the current actor and Agent policy authorize a load, Stella copies that one exact current revision into the active sandbox Session's temporary directory. That disposable execution projection contains only the selected revision and is removed with Session temporary data; it is not a backup or second authority.

### PostgreSQL-to-Home Skill migration

An upgrade with existing PostgreSQL Skill files stops at startup rather than rebuilding or discarding them. Stop every process that can write Skills, verify a restorable PostgreSQL backup and a backup of `$STELLA_HOME`, then consult `stellad storage migrate-skills --help`. The dedicated migration is one-way and Linux/macOS only. It inventories the legacy rows twice, publishes and verifies exact Home revisions, then scrubs the legacy file bytes and records completion in one database transaction.

The command is a dry-run unless you pass `--apply` with both required attestations. A dry-run does not publish Home revisions or selectors, write Skill migration evidence or completion, or scrub legacy Skill bytes. It is **not** process-wide or database-wide read-only: loading configuration can bootstrap the embedded PostgreSQL runtime, and opening the database can apply ordinary schema migrations.

### Retained revision capacity

Immutable revisions are retained indefinitely. Stella currently has no automatic pruning, per-Skill quota, deployment quota, or supported manual-pruning procedure. One complete revision is bounded to 512 files and 32 MiB. Catalog and revision reads are deliberately bounded, so a limit error means usage may be larger than Stella inspected; it is not evidence that storage is healthy.

Independently monitor free bytes and inodes on the filesystem that carries `$STELLA_HOME`. Do not delete retained revision directories directly: an older exact digest can still be required to reconcile an uncertain compare-and-swap retry or preserve Reflect provenance. Restore and repair authority only while Stella is stopped and from a verified backup.

Before upgrade, use the old working binary to import each custom Skill root under legacy top-level `.agents/skills/` as a global (`system`) Skill through **Settings → Skills** on older releases or **Admin Console → Deployment resources → Global Skills** on newer releases. Back up, verify, and remove other residual paths. New startup lists every blocking path and stops without changing or deleting anything. Paths owned by the current release manifest are inert even when contents or modes are stale; every other Skill root or residual path blocks.

Before downgrade, re-enable every disabled Skill and clear any dangling disabled references. Older binaries may ignore AgentSkillPolicy v1 and overwrite it during ordinary Agent edits. Mixed-version Skill activation is a product preference, not a security guarantee or filesystem access control.

These directories are rebuilt automatically and can live on ephemeral disk:

- **Builtin bundle** (`bundles/{revision}/`): installed from the running binary's immutable release bundle.
- **Session Skill projections**: authorized exact revisions copied into one active Session's temporary directory and removed with that temporary data. The typed Home roots listed above are durable authority, not cache.
- **`bin/`**: embedded tools and the `stella` CLI, re-extracted at startup.
- **Toolchains** (`.mise-tools/`, per-user `.mise-tools/`): re-installed on demand.
- **`pg-runtime/`**: the downloaded embedded-PostgreSQL runtime; re-download with `stellad postgres download`. Each runtime version installs into its own directory and older ones are never removed automatically — a few hundred megabytes each. Run `stellad postgres prune` to see what is unused, and again with `--force` to remove it.
- **`users/{id}/data/.cache/`**: per-user tool cache.

## Scratch

`dumps/` holds diagnostic dumps written when the process receives a debug signal. It is never read back by Stella and is safe to lose.

`cache/sandbox-tmp/` backs Docker sandbox sessions and is scratch space. A legacy `stella.db` file, if present, is only read by the one-time SQLite-to-PostgreSQL migration tool and is untouched by the running server.

`runner-scratch/` is a trusted host-owned structural namespace for disposable user-less-run workspaces. Normal runner close and construction failure perform best-effort cleanup, but a process crash or trusted host tampering can leave child directories behind. Isolating providers mount only the exact `runner-*` child, never the structural parent. Scratch is not a Principal or Agent Home and is never durable authority. Operators should remove leftovers only while Stella is stopped or affected consumers are fenced.
