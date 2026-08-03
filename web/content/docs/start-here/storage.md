---
title: Storage & Durability
---

Everything Stella writes to disk lives under `$STELLA_HOME` (default `~/.stella`, override with `STELLA_HOME`). On a single host with a persistent disk you never have to think about this page. On Kubernetes, on a multi-replica deployment, or anywhere the pod's disk is ephemeral, you do: each directory is either **durable data** you must keep, **derived cache** that rebuilds itself, or **scratch** that is safe to lose.

This page classifies every directory and tells you the volume and backup treatment each one needs. For the environment variables referenced below (`STELLA_DATABASE_URL`, `STELLA_BLOB_S3_*`), see the [environment-variable table on the Deployment page](/docs/start-here/deployment#environment-variables).

## Classification at a glance

| Path under `$STELLA_HOME`                                                                                                                                  | Holds                                                 | Classification | Kubernetes / ephemeral-disk treatment                                             |
| ---------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------- | -------------- | --------------------------------------------------------------------------------- |
| `postgres/`                                                                                                                                                | Embedded PostgreSQL cluster — the source of truth     | Durable        | Persistent volume **and** back it up. Absent when you set `STELLA_DATABASE_URL`.  |
| `users/{id}/data/assets/`                                                                                                                                  | User-uploaded files (per user)                        | Durable\*      | Persistent volume — **unless** the S3 mirror is configured (see below).           |
| `users/group-{id}/data/assets/`                                                                                                                            | User-uploaded files (per group)                       | Durable\*      | Same as per-user assets.                                                          |
| `users/{id}/agents/{id}/`                                                                                                                                  | Agent working trees and project files                 | Durable        | Persistent volume **and** pin to a single replica. Not mirrored anywhere.         |
| `library/`                                                                                                                                                 | Legacy article mirror (being drained into PostgreSQL) | Legacy         | Keep on a volume until the backfill reports zero missing, then archive or delete. |
| `bundles/{revision}/`                                                                                                                                      | Exact release-provided builtin Skill bundle           | Derived cache  | Reinstalled from the matching binary; do not modify it.                           |
| `.agents/skills/`                                                                                                                                          | Legacy Skill inventory                                | Migration gate | Preserve custom roots until they are imported or safely removed.                  |
| `.agents/db-skills/`, `agents/{agent-id}/.agents/skills/`, `users/{principal}/data/.agents/skills/`, `users/{principal}/agents/{agent-id}/.agents/skills/` | PostgreSQL-derived mutable Skill mirrors              | Derived cache  | Re-materialized on load; ephemeral disk is fine.                                  |
| `bin/`                                                                                                                                                     | Embedded tools and the `stella` CLI                   | Derived cache  | Ephemeral disk is fine. Re-extracted at startup.                                  |
| `.mise-tools/`, `users/{id}/.mise-tools/`                                                                                                                  | Toolchains for the sandbox                            | Derived cache  | Ephemeral disk is fine. Re-installed on demand.                                   |
| `pg-runtime/`                                                                                                                                              | Downloaded and extracted embedded-PostgreSQL runtime  | Derived cache  | Ephemeral disk is fine. Re-download with `stellad postgres download`.     |
| `users/{id}/data/.cache/`                                                                                                                                  | Per-user tool cache                                   | Derived cache  | Ephemeral disk is fine.                                                           |
| `cache/sandbox-tmp/`                                                                                                                                       | Docker sandbox temporary directories                  | Scratch        | Ephemeral disk is fine; stale directories are removed at startup.                 |
| `dumps/`                                                                                                                                                   | Diagnostic dumps written on signal                    | Scratch        | Ephemeral disk is fine. Diagnostic only.                                          |

\* Durable on local disk by default; becomes recoverable cache once the S3 mirror is configured — see [User assets](#user-assets-durable-or-mirrored).

## PostgreSQL is the source of truth (durable)

PostgreSQL holds nearly all state: configuration, secrets metadata, message history and summaries, mutable Skill records, Recally articles and their bodies, the fetched-models cache, goals, schedules, and the scheduler queue. Preserve it together with durable project Skill data; neither can be reconstructed.

- **Embedded cluster (default):** the data lives in `$STELLA_HOME/postgres/`. This directory must sit on a persistent volume and be backed up (stop the server first, or use a filesystem snapshot). The downloaded runtime under `pg-runtime/` is just code and can be re-fetched.
- **External server (`STELLA_DATABASE_URL`):** the database moves out of `$STELLA_HOME` entirely. Back it up with `pg_dump` against your database. This is the recommended setup for Kubernetes — it takes the single hardest-to-manage stateful directory off the pod.

## User assets (durable, or mirrored)

Files users upload are written to `users/{id}/data/assets/` (and `users/group-{id}/data/assets/` for groups). How you treat this tree depends on whether the S3 mirror is configured:

- **Without S3** (`STELLA_BLOB_S3_*` unset): the local copy is the only copy. This tree is durable data and needs a persistent volume; losing the disk loses the files.
- **With S3** (all four `STELLA_BLOB_S3_*` variables set): every write is mirrored to the bucket, a read that misses locally restores the file from the bucket, and a cold pod re-hydrates its assets from the bucket at session setup. The local tree becomes a recoverable cache, so pods can run on ephemeral disk and the bucket is what you back up.

Configuring the mirror is what lets asset-serving replicas be stateless. Set all four required S3 variables together — partial configuration fails startup.

## Agent working trees (durable, single-replica)

`users/{id}/agents/{id}/` (and the group equivalent) holds each agent's mutable working tree and project files. Nothing mirrors this to PostgreSQL or S3, so it is durable data: it needs a persistent volume, and because there is no distributed coordination over these trees today, pin the workload to a single replica. Multi-replica execution with checkpointing is future work — do not assume it yet.

## Legacy article mirror (draining)

`library/` is a leftover from when Recally article bodies were stored as files on disk. Bodies now live in PostgreSQL, and the only thing that still reads these files is a startup job that backfills any file-only bodies into the database — article reads serve exclusively from PostgreSQL and never fall back to disk. Nothing writes new files here. Keep the directory on a volume until a backfill run logs zero missing bodies; after that the files are inert legacy data and are safe to archive or delete.

## Skills and derived cache

Builtin Skills are the exact release bundle at `bundles/{revision}/`. Native `local` and `none` execution installs that bundle; isolating execution reads it at `/opt/stella/skills/builtin`. The `/opt` path is an execution coordinate, not a second content authority.

Project Skills are ordinary files in durable Agent/project working trees. PostgreSQL is the authority for mutable `system`, `system_agent`, `user`, and `user_agent` records; `.agents/db-skills/`, `agents/{agent-id}/.agents/skills/`, `users/{principal}/data/.agents/skills/`, and `users/{principal}/agents/{agent-id}/.agents/skills/` are derived mirrors re-materialized on load. Here, `{principal}` is a user ID or `group-{id}`.

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
