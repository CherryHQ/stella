---
title: Object Storage Backend (Design)
---

> This is a design proposal for [#481](https://github.com/vaayne/stella/issues/481), a sub-issue of #477 (production deployment). It is an explanation of _why_ the design is shaped this way, not an implementation guide. No code exists yet.

## The trap to avoid

The issue asks to "add S3-compatible storage for persistent files." Read literally, that suggests swapping the local filesystem for S3. That is a category error.

Stella's storage is **sandbox-filesystem-centric**. The sandbox mounts `users/{userID}/agents/{agentID}/`, `projects/{projectID}/`, `.mise-tools/`, and `.cache/` as the agent's working directory (`/workspace`, `/user`). Agents read and write those paths in place — through the `write`/`edit`/`read` tools, mise toolchains, and git working trees. That demands real POSIX semantics: partial writes, rename, mmap, executable bits.

S3 is an object store: `PUT`/`GET`/`DELETE` by key, no partial writes, no rename, no POSIX. **It cannot back a live sandbox filesystem.** Putting all of `STELLA_HOME` on S3 is not viable.

So the real question is narrower than the issue title.

## What multi-replica actually breaks

The production pain isn't "the sandbox needs to be shared." A session's sandbox is per-run and can stay pinned to a node. The pain is the **durable blob tier**: when replica A handles an upload and replica B serves it later, local disk doesn't share state.

That tier — and only that tier — is what this design moves to object storage.

| Belongs on object storage (write-once, read-many) | Stays on local FS (live POSIX state)          |
| ------------------------------------------------- | --------------------------------------------- |
| Channel/user uploads — `data/assets/`             | Sandbox working trees, project working tree   |
| Email attachments — `internal/email/imap.go`      | `.mise-tools/`, `.cache/`                     |
| Knowledge-base articles — `internal/recally/`     | Skill disk mirror (sandbox reads it in place) |
| Exported/durable artifacts                        | git repositories                              |

Sharing the sandbox tier across replicas (node-pinned ephemeral disk + rehydrate-from-S3 on session start, or an RWX volume) is a **#477 / Kubernetes** concern. It is explicitly out of scope here.

## The abstraction

A new package `internal/objstore` defines a backend-agnostic interface. Two implementations: `local` (default, filesystem-backed) and `s3` (via [minio-go](https://github.com/minio/minio-go), which treats AWS S3, MinIO, and Cloudflare R2 uniformly and keeps the dependency tree small).

```go
type Store interface {
    Put(ctx context.Context, key string, r io.Reader, opts PutOptions) error
    Get(ctx context.Context, key string) (io.ReadCloser, error)
    Delete(ctx context.Context, key string) error
    Stat(ctx context.Context, key string) (ObjectInfo, error)
    // SignedURL returns a time-bound direct URL. ok=false when the backend
    // cannot sign (the local backend), so callers fall back to proxying.
    SignedURL(ctx context.Context, key string, ttl time.Duration) (url string, ok bool)
}
```

The interface is deliberately a deep module: a tiny surface (five methods) hiding all backend-specific concerns (multipart upload, path-style vs virtual-host addressing, content-type negotiation). Callers never branch on backend type.

## Keys must not leak names

Object keys are an addressing scheme, not a place to encode identity. The issue calls out "avoid leaking sensitive user/project names." So keys are opaque:

```
objects/{tenant}/{uuid-or-sha256}
```

The human filename, owner (user/agent/project), content-type, and size live in a DB metadata table `blob_object`, never in the key. A logical object maps to a storage key through that table, which decouples the key scheme from the on-disk FS layout entirely. Content-addressing (`sha256`) enables dedup; random UUID is simpler — that choice is deferred to implementation.

## Access control sits above the store, not inside it

`objstore.Store` does no authorization — it's a dumb blob mover. Permission checks live in the HTTP serving layer:

1. Resolve the logical object → look up `blob_object`, get owner + storage key.
2. Authorize the caller against the owner (the existing Stella permission model).
3. Serve the bytes.

Step 3 picks one of two paths, decided by backend capability:

- **Sign if you can, proxy otherwise.** Call `SignedURL`. If `ok`, redirect (302) to a short-lived presigned URL — the client pulls straight from S3, saving stellad's bandwidth on large files. If `!ok` (local backend), stream the bytes through stellad after the auth check.

This keeps a single serving entrypoint regardless of backend, and never exposes an unsigned, unauthorized object URL.

## Configuration

A new `storage` config section selects the backend and carries S3 parameters:

- `backend`: `local` (default) | `s3`
- S3 params: `endpoint`, `region`, `bucket`, `access_key`, `secret_key`, `path_style` (true for MinIO/R2), `use_ssl`
- Each backed by an env var override, consistent with the existing config package (`internal/config`).

`local` remains the zero-config default so simple self-hosted deployments are unaffected.

## Migration

A CLI command (`stella storage migrate`) walks the existing local blob directories (`data/assets/`, `library/`, email attachments), uploads each to the configured backend, writes the `blob_object` metadata row, and — after a verification pass — optionally removes the local copy. Idempotent: re-running skips objects already present in metadata.

## Retention and deletion

Deleting a logical object removes its `blob_object` row and the underlying object. Whether to hard-delete immediately or tombstone for a grace period is a policy decision deferred to implementation; the interface supports either.

## Out of scope

- Sharing the sandbox/working-tree filesystem across replicas (→ #477 / K8s).
- Migrating the SQLite database to external storage (→ #477 PostgreSQL track).
- Skill files — already DB-backed with a disk mirror the sandbox consumes in place; object storage adds nothing there.
