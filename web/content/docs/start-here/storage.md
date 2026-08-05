---
title: Storage & Durability
---

Everything Stella writes to disk lives under `$STELLA_HOME` (default `~/.stella`, override with `STELLA_HOME`). On a single host with a persistent disk you never have to think about this page. On Kubernetes, on a multi-replica deployment, or anywhere the pod's disk is ephemeral, you do: each directory is either **durable data** you must keep, **derived cache** that rebuilds itself, or **scratch** that is safe to lose.

This page classifies every directory and tells you the volume and backup treatment each one needs. For the environment variables referenced below (`STELLA_DATABASE_URL`, `STELLA_BLOB_S3_*`), see the [environment-variable table on the Deployment page](/docs/start-here/deployment#environment-variables).

## Classification at a glance

| Path under `$STELLA_HOME`                                                                       | Holds                                                 | Classification | Kubernetes / ephemeral-disk treatment                                             |
| ----------------------------------------------------------------------------------------------- | ----------------------------------------------------- | -------------- | --------------------------------------------------------------------------------- |
| `postgres/`                                                                                     | Embedded PostgreSQL cluster — the source of truth     | Durable        | Persistent volume **and** back it up. Absent when you set `STELLA_DATABASE_URL`.  |
| `users/{id}/data/`                                                                              | User Principal Home: user data and uploads            | Durable        | Persistent volume **and** pin to a single replica.                                |
| `users/group-{id}/data/`                                                                        | Group Principal Home: group data and uploads          | Durable        | Same as per-user Principal data.                                                  |
| `users/{principal}/agents/{id}/`                                                                | Per-principal Agent Home: workspace and project files | Durable        | Persistent volume **and** pin to a single replica.                                |
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

Principal Home and Agent Home bytes are durable data and the live authority for their mutable files. Back up their storage with PostgreSQL.

## PostgreSQL is the source of truth (durable)

PostgreSQL holds nearly all state: configuration, secrets metadata, message history and summaries, mutable Skill records, Recally articles and their bodies, the fetched-models cache, goals, schedules, and the scheduler queue. Preserve it together with durable project Skill data; neither can be reconstructed.

Phase 1 also records typed Home identity and lifecycle metadata in PostgreSQL: user and group Principal Homes, per-principal Agent Homes, and the narrow system and system-Agent Skill roots. That stable metadata does **not** make Home file bytes recoverable. Back up PostgreSQL together with every durable Principal Home and Agent Home storage location.

- **Embedded cluster (default):** the data lives in `$STELLA_HOME/postgres/`. This directory must sit on a persistent volume and be backed up (stop the server first, or use a filesystem snapshot). The downloaded runtime under `pg-runtime/` is just code and can be re-fetched.
- **External server (`STELLA_DATABASE_URL`):** the database moves out of `$STELLA_HOME` entirely. Back it up with `pg_dump` against your database. This is the recommended setup for Kubernetes — it takes the single hardest-to-manage stateful directory off the pod.

## User assets (durable Principal Home files)

Files users upload are written to `users/{id}/data/assets/` (and `users/group-{id}/data/assets/` for groups). They are ordinary durable files in the owning Principal Home.

Uploads, shared artifacts, channel attachments, and project/session operations use these Home files. They do not mirror, cache, hydrate, restore, roll back, or delete mutable assets in object storage.

Persist and back up each Principal Home. If configured, back up the object store for immutable content-addressed session media. Principal Home and Agent Home storage remains single-replica durable storage until a later Compose or Kubernetes topology gate.

### S3 asset migration gate

When the complete `STELLA_BLOB_S3_*` configuration identifies legacy mutable-asset authority, every object-only mutable asset must first exist in the correct user or group Principal Home. Startup fails closed while that migration is pending, including when the configured bucket is empty. It does not delete, move, or guess at missing data.

To clear the gate safely:

1. Stop every old Stella binary, service, pod, and job that can write assets.
2. Keep the original `STELLA_BLOB_S3_*` values and database configuration available. Back up the bucket, PostgreSQL, and Principal Home storage.
3. Run `stellad storage migrate-assets --help`, then perform the documented dry run.
4. Run the real migration. It maps bucket keys only through known user and group identities, verifies file count, byte count, and SHA-256 content digests, and leaves every remote object untouched.
5. Start Stella only after the command reports the migration marker as complete.

The command is idempotent: after an interruption, stop writers again and rerun it. A conflicting local file fails the migration rather than being overwritten. After the marker is complete, Principal Homes are the live mutable-asset authority. Remote legacy objects remain untouched but are not a runtime authority or fallback. Keep the blob-store configuration: the same object store remains the authority for immutable content-addressed session media.

## Principal and Agent Homes (durable, single-replica)

The current local store preserves compatibility paths: `users/{id}/data/` and `users/group-{id}/data/` are user and group Principal Homes; `users/{principal}/agents/{id}/` is each Principal's Agent Home. An Agent Home holds that principal's mutable working tree and project files. Home bytes are not recoverable from PostgreSQL or object storage. They are durable data: use persistent storage and pin the workload to one replica. Multi-replica execution with checkpointing is future work.

The paths are current local compatibility coordinates, not Home identity. A Home has stable registry metadata, including its Store ID and opaque locator, so future storage implementations need not preserve these path shapes.

## Destructive owner deletion

An explicit destructive deletion of a user, group, or Agent immediately tombstones its Homes and fences local cached execution. A shared worker then purges physical bytes asynchronously and idempotently. This is the only Home-deleting lifecycle: removing an Agent assignment, removing a group member, archiving a Session, and uninstalling Helm do **not** delete Homes.

If physical purge fails, the Home remains in `purge_failed` with its audit record. It is not silently discarded; an operator must retry it. For syntax, run `stellad storage retry-purge --help`.

## Legacy article mirror (draining)

`library/` is a leftover from when Recally article bodies were stored as files on disk. Bodies now live in PostgreSQL, and the only thing that still reads these files is a startup job that backfills any file-only bodies into the database — article reads serve exclusively from PostgreSQL and never fall back to disk. Nothing writes new files here. Keep the directory on a volume until a backfill run logs zero missing bodies; after that the files are inert legacy data and are safe to archive or delete.

## Skills and derived cache

Builtin Skills are the exact release bundle at `bundles/{revision}/`. Native `local` and `none` execution installs that bundle; isolating execution reads it at `/opt/stella/skills/builtin`. The `/opt` path is an execution coordinate, not a second content authority.

Project Skills are ordinary files in durable Agent/project working trees. PostgreSQL is the authority for mutable `system`, `system_agent`, `user`, and `user_agent` records; `.agents/db-skills/`, `agents/{agent-id}/.agents/skills/`, `users/{principal}/data/.agents/skills/`, and `users/{principal}/agents/{agent-id}/.agents/skills/` are derived mirrors re-materialized on load. Here, `{principal}` is a user ID or `group-{id}`. Phase 1 registers typed Home identities but does not cut over mutable Skill content authority.

Before upgrade, import each custom Skill root under legacy top-level `.agents/skills/` through **Settings → Skills** as a global (`system`) Skill using the old working binary. Back up, verify, and remove other residual paths. New startup lists every blocking path and stops without changing or deleting anything. Paths owned by the current release manifest are inert even when contents or modes are stale; every other Skill root or residual path blocks.

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
