---
title: Qualify Shared POSIX Storage
---

Stella's default `local` storage mode supports one server using one persistent POSIX `STELLA_HOME`. Before selecting `shared-posix`, qualify the exact backend version, topology, client/node versions, and mount options that you will deploy. A `ReadWriteMany` claim, a successful mount, or a local bind/Compose volume is not qualification.

Shared mode still does **not** enable multiple Stella replicas. The Helm chart keeps `replicaCount: 1` until the independent distributed-runtime lifecycle prerequisite and later multi-replica conformance are complete.

## Deployment contract

A qualified deployment provides one global, strongly consistent namespace at the same logical `STELLA_HOME` root to every `stellad` replica and Session compute client. Every client uses the same numeric UID/GID and permission model. Session compute receives only its authorized Principal data, Agent workspace, and read-only Skill views at Stella's fixed guest coordinates—never the namespace root or another Principal's roots. Pod/node placement must not determine where durable bytes live, and there is no replica-local fallback.

The backend must preserve same-directory atomic rename, relative symlinks and rooted containment, modes/ownership, cross-client advisory locking, atomic append records, concurrent read/write visibility, close-to-open consistency, and file plus directory `fsync` durability. An interrupted write after bytes may have changed is **outcome unknown**: callers must inspect/reconcile the exact operation and must not replay it automatically.

JuiceFS Community Edition is the recommended reference implementation, not an application dependency. EFS, NFS, CephFS, and other backends are eligible only when their exact topology passes the same gates.

## 1. Declare criteria before running

Mount the same candidate namespace independently at two real, non-symlink client paths. Create a JSON input such as:

```json
{
  "client_a": "/mnt/client-a/stella",
  "client_b": "/mnt/client-b/stella",
  "metadata": {
    "backend": "juicefs-ce",
    "version": "1.4.1",
    "topology": "two clients; shared metadata and object store",
    "clients": 2,
    "nodes": 2,
    "mount_options": ["default_permissions"],
    "namespace_identity": "production-home-v1",
    "identity_mechanism": "backend volume UUID plus Stella identity file",
    "reference_hardware": "record node, CPU, memory, network, metadata, and object-store classes",
    "independent_mounts": true
  },
  "limits": {
    "metadata_p95_ms": 50,
    "small_files_p95_ms": 250,
    "concurrent_p95_ms": 250,
    "stream_mib_per_second": 25,
    "minimum_free_bytes": 10737418240
  },
  "failure_injection": {
    "injected": true,
    "disconnect_observed": true,
    "remounted": true,
    "revalidated": true,
    "error_class": "outcome_unknown",
    "outcome_unknown": true,
    "detail": "record the exact disconnect, readiness, remount, and recovery procedure"
  }
}
```

Choose limits from Stella's production latency/capacity budget before execution; do not loosen them after seeing results. Every latency sample uses a distinct tree rather than a warmed path. The fixed workloads cover durable typed-root materialization; 16-file project/Skill publication using synced temporary files, atomic revision rename, and parent-directory sync; a synced 4 MiB upload plus verified peer stream; and eight concurrent durable API-writer/sandbox-reader pairs. The record includes measured p95 latency, streaming throughput, and free-capacity verdicts.

The failure evidence is an operator attestation because disconnect/remount is topology-specific. Perform it for real: interrupt one client during a write, verify the error is classified outcome-unknown, verify readiness/admission closes, remount, and verify full identity, qualification, read/write, and cross-client freshness validation precedes recovery. A fabricated attestation invalidates the record.

## 2. Run and review qualification

```bash
stellad storage qualify --config qualification-input.json --output qualification.json
```

Run the same harness first against a local POSIX control. That control should pass semantic cases but must end with `qualified_shared: false`; a symlink alias, divergent roots, read-only mount, or incompatible backend must not qualify. Then run it on at least two independent mounts of the candidate backend. Preserve the input, output, backend/client versions, hardware, and failure-injection logs together. Approval requires every conformance and benchmark item, `qualified_shared`, and `overall_pass` to be `true`.

Install only a reviewed passing record into the shared namespace:

```bash
stellad storage install-qualification --record qualification.json --root /mnt/client-a/stella
sha256sum /mnt/client-a/stella/.stella-shared-posix/qualification.json
```

The command writes the fixed namespace identity and exact record. Use the displayed SHA-256 as `STELLA_SHARED_POSIX_QUALIFICATION_SHA256`. Changing the backend version, topology, mount options, identity, clients, or limits requires a new record and digest.

Installation and runtime parse the same strict record contract. Unsupported schemas, unknown fields, missing, duplicated, failed, or inconsistent identity/conformance/benchmark/failure/recovery/readiness evidence are rejected even when the file's configured digest matches. The digest pins reviewed bytes; it does not replace semantic validation.

## 3. Run the independent freshness witness

On a different client/node from every `stellad` process, run one supervised witness against its independent mount:

```bash
stellad storage witness --root /mnt/witness/stella --client-id storage-witness-a --interval 2s
```

The witness atomically advances a sequence in the shared namespace. Running it beside Stella or against Stella's same mount does not prove cross-client freshness. Treat witness loss as an availability failure: Stella intentionally becomes not ready and rejects new durable-filesystem and Session-compute admission.

## 4. Enable shared mode

Set these on every Stella server:

```text
STELLA_STORAGE_MODE=shared-posix
STELLA_SHARED_POSIX_IDENTITY=production-home-v1
STELLA_SHARED_POSIX_QUALIFICATION_SHA256=<64 hex characters>
STELLA_SHARED_POSIX_WITNESS_ID=storage-witness-a
STELLA_STORAGE_CHECK_INTERVAL=2s
STELLA_STORAGE_FRESHNESS_TIMEOUT=15s
STELLA_STORAGE_STARTUP_TIMEOUT=20s
```

Startup occurs only after the root object, identity, exact qualification digest, writable/fsync probe, and two advancing witness observations pass. Thereafter the monitor repeats full checks. Missing, replaced, disconnected, read-only, stale, or mismatched storage makes `/readyz` return an actionable path-free `503` and closes the one Home admission gate. New Workspace/API filesystem capabilities, Session compute setup, managed Home tool execution, and Home-backed diagnostics fail; Stella never creates or uses local fallback storage. Independently configured PostgreSQL, object storage, and provider-only processing remain available. Liveness remains process-only.

`STELLA_STORAGE_STARTUP_TIMEOUT` is an overall startup deadline, including a blocked mount probe. POSIX mount syscalls are not generically cancellable: Stella therefore permits at most one probe worker. If that syscall remains stuck, startup fails at the deadline (and process exit releases it); at runtime the last successful check expires, readiness/admission closes, and no overlapping probe workers are launched. A later return cannot reopen admission by itself—full validation and a subsequent fresh witness advance are still required.

After a transient failure, readiness returns only after a complete successful revalidation and a fresh witness advance. A mount replacement or explicit unmount/remount requires restarting `stellad` so `WorkspaceManager` can pin the newly validated root object; the old process remains fail-closed. Existing operations are not silently replayed. Monitor free bytes and inodes separately; qualification's capacity check is a point-in-time gate, not capacity monitoring.

For Helm, set `persistence.sharedPOSIX.enabled=true`, `persistence.accessMode=ReadWriteMany`, the three evidence values, and the timing values. Keep `replicaCount=1`. The chart does not provision the backend or witness and rejects shared mode with ephemeral/local-only persistence.
