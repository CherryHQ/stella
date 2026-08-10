---
title: Storage & Durability
---

Everything Stella writes to disk lives under `$STELLA_HOME` (default `~/.stella`, override with `STELLA_HOME`). On a single host with a persistent disk you never have to think about this page. On Kubernetes, on a multi-replica deployment, or anywhere the pod's disk is ephemeral, you do: each directory is either **durable data** you must keep, **derived cache** that rebuilds itself, or **scratch** that is safe to lose.

This page classifies every directory and tells you the volume and backup treatment each one needs. For the environment variables referenced below (`STELLA_DATABASE_URL`, `STELLA_BLOB_S3_*`), see the [environment-variable table on the Deployment page](/docs/start-here/deployment#environment-variables).

## Classification at a glance

| Path under `$STELLA_HOME`                                                                       | Holds                                                 | Classification | Kubernetes / ephemeral-disk treatment                                             |
| ----------------------------------------------------------------------------------------------- | ----------------------------------------------------- | -------------- | --------------------------------------------------------------------------------- |
| `postgres/`                                                                                     | Embedded PostgreSQL cluster — the source of truth     | Durable        | Persistent volume **and** back it up. Absent when you set `STELLA_DATABASE_URL`.  |
| `users/{id}/data/`                                                                              | User Principal Home: user data and uploads            | Durable\*      | Persistent volume **and** pin to a single replica; only assets can be mirrored.   |
| `users/group-{id}/data/`                                                                        | Group Principal Home: group data and uploads          | Durable\*      | Same as per-user Principal data.                                                  |
| `users/{principal}/agents/{id}/`                                                                | Per-principal Agent Home: workspace and project files | Durable        | Persistent volume **and** pin to a single replica. Not mirrored anywhere.         |
| `library/`                                                                                      | Legacy article mirror (being drained into PostgreSQL) | Legacy         | Keep on a volume until the backfill reports zero missing, then archive or delete. |
| `bundles/{revision}/`                                                                           | Exact release-provided builtin Skill bundle           | Derived cache  | Reinstalled from the matching binary; do not modify it.                           |
| `.agents/skills/`                                                                               | Legacy Skill inventory                                | Migration gate | Preserve custom roots until they are imported or safely removed.                  |
| `.agents/db-skills/`, `agents/{agent-id}/.agents/skills/`                                       | Narrow system and system-Agent Skill roots            | Derived cache  | PostgreSQL-derived, re-materialized on load; ephemeral disk is fine.              |
| `users/{principal}/data/.agents/skills/`, `users/{principal}/agents/{agent-id}/.agents/skills/` | Principal and Agent mutable Skill mirrors             | Derived cache  | PostgreSQL-derived, re-materialized on load; ephemeral disk is fine.              |
| `bin/`                                                                                          | Embedded tools and the `stella` CLI                   | Derived cache  | Ephemeral disk is fine. Re-extracted at startup.                                  |
| `.mise-tools/`, `users/{id}/.mise-tools/`                                                       | Toolchains for the sandbox                            | Derived cache  | Ephemeral disk is fine. Re-installed on demand.                                   |
| `pg-runtime/`                                                                                   | Downloaded and extracted embedded-PostgreSQL runtime  | Derived cache  | Ephemeral disk is fine. Re-download with `stellad postgres download`.             |
| `users/{id}/data/.cache/`                                                                       | Per-user tool cache                                   | Derived cache  | Ephemeral disk is fine.                                                           |
| `cache/sandbox-tmp/`                                                                            | Docker sandbox temporary directories                  | Scratch        | Ephemeral disk is fine; stale directories are removed at startup.                 |
| `dumps/`                                                                                        | Diagnostic dumps written on signal                    | Scratch        | Ephemeral disk is fine. Diagnostic only.                                          |

\* Principal data and Agent Homes remain durable. Uploaded assets within a Principal Home become recoverable cache once the S3 mirror is configured — see [User assets](#user-assets-durable-or-mirrored).

## PostgreSQL is the source of truth (durable)

PostgreSQL holds nearly all state: configuration, secrets metadata, message history and summaries, mutable Skill records, Recally articles and their bodies, the fetched-models cache, goals, schedules, and the scheduler queue. Preserve it together with durable project Skill data; neither can be reconstructed.

Phase 1 also records typed Home identity and lifecycle metadata in PostgreSQL: user and group Principal Homes, per-principal Agent Homes, and the narrow system and system-Agent Skill roots. That stable metadata does **not** make Home file bytes recoverable. Back up PostgreSQL together with every durable Principal Home and Agent Home storage location.

- **Embedded cluster (default):** the data lives in `$STELLA_HOME/postgres/`. This directory must sit on a persistent volume and be backed up (stop the server first, or use a filesystem snapshot). The downloaded runtime under `pg-runtime/` is just code and can be re-fetched.
- **External server (`STELLA_DATABASE_URL`):** the database moves out of `$STELLA_HOME` entirely. Back it up with `pg_dump` against your database. This is the recommended setup for Kubernetes — it takes the single hardest-to-manage stateful directory off the pod.

## User assets (durable, or mirrored)

Files users upload are written to `users/{id}/data/assets/` (and `users/group-{id}/data/assets/` for groups). How you treat this tree depends on whether the S3 mirror is configured:

- **Without S3** (`STELLA_BLOB_S3_*` unset): the local copy is the only copy. This tree is durable data and needs a persistent volume; losing the disk loses the files.
- **With S3** (all four `STELLA_BLOB_S3_*` variables set): every write is mirrored to the bucket, a read that misses locally restores the file from the bucket, and a cold pod re-hydrates its assets from the bucket at session setup. The local tree becomes a recoverable cache, so pods can run on ephemeral disk and the bucket is what you back up.

Configuring the mirror is what lets asset-serving replicas be stateless. Set all four required S3 variables together — partial configuration fails startup. Startup records whether mutable asset object authority is configured as migration metadata only; Phase 1 does not change mirror/hydrate behavior or authority.

## Principal and Agent Homes (durable, single-replica)

The current local store preserves compatibility paths: `users/{id}/data/` and `users/group-{id}/data/` are user and group Principal Homes; `users/{principal}/agents/{id}/` is each Principal's Agent Home. An Agent Home holds that principal's mutable working tree and project files. Nothing mirrors Principal data or Agent Home bytes to PostgreSQL, and S3 only mirrors assets as described above. They are durable data: use persistent storage and pin the workload to one replica. Multi-replica execution with checkpointing is future work — do not assume it yet.

The paths are current local compatibility coordinates, not Home identity. A Home has stable registry metadata, including its Store ID and opaque locator, so future storage implementations need not preserve these path shapes.

PostgreSQL is the authority for Home identity and lifecycle; the configured Store is the authority for its file bytes. If a registry record is `ready` but its exact physical root is missing, symlinked, or replaced, Stella reports storage loss and does not recreate an empty root. Restore the Home bytes and PostgreSQL metadata together from a consistent backup.

## Destructive owner deletion

An explicit destructive group or Agent deletion immediately tombstones its Homes and fences local cached execution. Destructive user deletion has the same internal lifecycle primitive, but product account deletion is not integrated with it yet. A shared worker uses a durable, exclusive, expiring claim to purge physical bytes asynchronously and idempotently. The local Store also holds a per-Home operating-system lock until physical mutation returns, so an expired database claim cannot overlap stale local deletion; after a crash or expired claim, another worker can recover safely. A child failure or active claim durably snoozes its parent continuation without consuming the retry budget. These are the only Home-deleting lifecycles: removing an Agent assignment, removing a group member, archiving a Session, and uninstalling Helm do **not** delete Homes.

If physical purge fails, the Home remains in `purge_failed` with its audit record. It is not silently discarded; an operator must retry it. For syntax, run `stellad storage retry-purge --help`.

## Legacy article mirror (draining)

`library/` is a leftover from when Recally article bodies were stored as files on disk. Bodies now live in PostgreSQL, and the only thing that still reads these files is a startup job that backfills any file-only bodies into the database — article reads serve exclusively from PostgreSQL and never fall back to disk. Nothing writes new files here. Keep the directory on a volume until a backfill run logs zero missing bodies; after that the files are inert legacy data and are safe to archive or delete.

## Skills and derived cache

Builtin Skills are the exact release bundle at `bundles/{revision}/`. Native `local` and `none` execution installs that bundle; isolating execution reads it at `/opt/stella/skills/builtin`. The `/opt` path is an execution coordinate, not a second content authority.

Project Skills are ordinary files in durable Agent/project working trees. PostgreSQL is the authority for mutable `system`, `system_agent`, `user`, and `user_agent` records; `.agents/db-skills/`, `agents/{agent-id}/.agents/skills/`, `users/{principal}/data/.agents/skills/`, and `users/{principal}/agents/{agent-id}/.agents/skills/` are derived mirrors re-materialized on load. Here, `{principal}` is a user ID or `group-{id}`. Phase 1 registers typed Home identities but does not cut over mutable Skill content authority.

Before upgrade, use the old working binary to import each custom Skill root under legacy top-level `.agents/skills/` as a global (`system`) Skill through **Settings → Skills** on older releases or **Admin Console → Deployment resources → Global Skills** on newer releases. Back up, verify, and remove other residual paths. New startup lists every blocking path and stops without changing or deleting anything. Paths owned by the current release manifest are inert even when contents or modes are stale; every other Skill root or residual path blocks.

Before downgrade, re-enable every disabled Skill and clear any dangling disabled references. Older binaries may ignore AgentSkillPolicy v1 and overwrite it during ordinary Agent edits. Mixed-version Skill activation is a product preference, not a security guarantee or filesystem access control.

These directories are rebuilt automatically and can live on ephemeral disk:

- **Builtin bundle** (`bundles/{revision}/`): installed from the running binary's immutable release bundle.
- **PostgreSQL-derived Skill mirrors** (`.agents/db-skills/`, `agents/{agent-id}/.agents/skills/`, `users/{principal}/data/.agents/skills/`, and `users/{principal}/agents/{agent-id}/.agents/skills/`): re-materialized on load.
- **`bin/`**: embedded tools and the `stella` CLI, re-extracted at startup.
- **Toolchains** (`.mise-tools/`, per-user `.mise-tools/`): re-installed on demand.
- **`pg-runtime/`**: the downloaded embedded-PostgreSQL runtime; re-download with `stellad postgres download`. Each runtime version installs into its own directory and older ones are never removed automatically — a few hundred megabytes each. Run `stellad postgres prune` to see what is unused, and again with `--force` to remove it.
- **`users/{id}/data/.cache/`**: per-user tool cache.

## Scratch

`dumps/` holds diagnostic dumps written when the process receives a debug signal. It is never read back by Stella and is safe to lose.

`cache/sandbox-tmp/` backs Docker sandbox sessions and is scratch space. A legacy `stella.db` file, if present, is only read by the one-time SQLite-to-PostgreSQL migration tool and is untouched by the running server.
