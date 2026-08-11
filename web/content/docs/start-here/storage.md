---
title: Storage & Durability
---

Everything Stella writes to disk lives under `$STELLA_HOME` (default `~/.stella`, override with `STELLA_HOME`). On a single host with a persistent disk you never have to think about this page. On Kubernetes, on a multi-replica deployment, or anywhere the pod's disk is ephemeral, you do: each directory is either **durable data** you must keep, **derived cache** that rebuilds itself, or **scratch** that is safe to lose.

This page classifies every directory and tells you the volume and backup treatment each one needs. For the environment variables referenced below (`STELLA_DATABASE_URL`, `STELLA_BLOB_S3_*`), see the [environment-variable table on the Deployment page](/docs/start-here/deployment#environment-variables).

## Classification at a glance

| Path under `$STELLA_HOME`                                                                       | Holds                                                 | Classification | Kubernetes / ephemeral-disk treatment                                             |
| ----------------------------------------------------------------------------------------------- | ----------------------------------------------------- | -------------- | --------------------------------------------------------------------------------- |
| `postgres/`                                                                                     | Embedded PostgreSQL cluster — the source of truth     | Durable        | Persistent volume **and** back it up. Absent when you set `STELLA_DATABASE_URL`.  |
| `users/{id}/data/`                                                                              | User Principal Home: user data and uploads            | Durable        | Persistent POSIX storage; one replica uses local `$STELLA_HOME`.                  |
| `users/group-{id}/data/`                                                                        | Group Principal Home: group data and uploads          | Durable        | Same as per-user Principal data.                                                  |
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
| `runner-scratch/runner-*`                                                                       | Disposable workspace for user-less runs               | Scratch        | Never Home authority; clean leftovers only while Stella is stopped or fenced.     |
| `dumps/`                                                                                        | Diagnostic dumps written on signal                    | Scratch        | Ephemeral disk is fine. Diagnostic only.                                          |

## PostgreSQL is the source of truth (durable)

PostgreSQL holds nearly all state: configuration, secrets metadata, message history and summaries, mutable Skill records, Recally articles and their bodies, the fetched-models cache, goals, schedules, and the scheduler queue. Preserve it together with durable project Skill data; neither can be reconstructed.

Phase 1 also records typed Home identity and lifecycle metadata in PostgreSQL: user and group Principal Homes, per-principal Agent Homes, and the narrow system and system-Agent Skill roots. That stable metadata does **not** make Home file bytes recoverable. Back up PostgreSQL together with every durable Principal Home and Agent Home storage location.

- **Embedded cluster (default):** the data lives in `$STELLA_HOME/postgres/`. This directory must sit on a persistent volume and be backed up (stop the server first, or use a filesystem snapshot). The downloaded runtime under `pg-runtime/` is just code and can be re-fetched.
- **External server (`STELLA_DATABASE_URL`):** the database moves out of `$STELLA_HOME` entirely. Back it up with `pg_dump` against your database. This is the recommended setup for Kubernetes — it takes the single hardest-to-manage stateful directory off the pod.

## User assets (durable POSIX data)

Files users upload are written to `users/{id}/data/assets/` (and `users/group-{id}/data/assets/` for groups). This live mutable tree is part of the Principal Home and has the same durability requirements. Workspace APIs, channel attachment ingestion, and Agent mounts observe the same POSIX bytes.

`STELLA_BLOB_S3_*` configures immutable BlobStore content such as media, artifacts, and published Share snapshots. It does not make S3 the live workspace API, restore a missing mutable asset, or permit an ephemeral Principal Home. Back up the POSIX namespace even when S3 is configured.

Upgrades do not copy or delete legacy asset objects. Supported single-replica upgrades retain the existing persistent `$STELLA_HOME`, which already contains the local files written by previous versions. If a deployment has object-only mutable assets because the POSIX copy was independently lost, restore and verify those files before upgrading; Stella does not run a generic workspace migration or restore-on-miss path.

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

`runner-scratch/` is a trusted host-owned structural namespace for disposable user-less-run workspaces. Normal runner close and construction failure perform best-effort cleanup, but a process crash or trusted host tampering can leave child directories behind. Isolating providers mount only the exact `runner-*` child, never the structural parent. Scratch is not a Principal or Agent Home and is never durable authority. Operators should remove leftovers only while Stella is stopped or affected consumers are fenced.
