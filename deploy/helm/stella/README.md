# Stella Helm Chart

Deploys [Stella](https://github.com/CherryHQ/stella) on Kubernetes as a
single-replica `StatefulSet` backed by SQLite on a persistent volume.

> **Single-writer by design.** Stella currently stores all state in one SQLite
> database. This chart runs exactly one replica. Do not raise `replicaCount` or
> enable autoscaling — concurrent writers corrupt SQLite. Horizontal scaling
> arrives with the PostgreSQL backend ([#477](https://github.com/CherryHQ/stella/issues/477)).

## Prerequisites

- Kubernetes 1.25+
- Helm 3.8+
- A default StorageClass that provides `ReadWriteOnce` volumes (avoid NFS —
  SQLite WAL is unreliable over NFS)
- A vault key: `stella vault keygen`

## Install

```bash
# Required: the age vault key. Prefer --set-file or an existing Secret over
# putting the key in a values file.
helm install stella ./deploy/helm/stella \
  --namespace stella --create-namespace \
  --set-file secret.vaultKey=/dev/stdin <<<"$(stella vault keygen)" \
  --set secret.apiKeys.ANTHROPIC_API_KEY=sk-ant-...
```

Or with a values file:

```bash
helm install stella ./deploy/helm/stella -n stella --create-namespace -f my-values.yaml
```

Verify:

```bash
kubectl -n stella rollout status statefulset/stella
helm test stella -n stella
```

## Configuration

| Key                         | Default                   | Description                                               |
| --------------------------- | ------------------------- | --------------------------------------------------------- |
| `replicaCount`              | `1`                       | Keep at 1 (SQLite).                                       |
| `image.repository`          | `ghcr.io/cherryhq/stella` | Image repo.                                               |
| `image.tag`                 | `""` (chart appVersion)   | Image tag.                                                |
| `service.port`              | `25678`                   | stellad listen port.                                      |
| `sandbox.backend`           | `local`                   | `local`, `none`, or `docker`.                             |
| `persistence.enabled`       | `true`                    | Provision a PVC for `STELLA_HOME`.                        |
| `persistence.size`          | `10Gi`                    | Volume size.                                              |
| `persistence.storageClass`  | `""`                      | StorageClass (cluster default if empty).                  |
| `persistence.existingClaim` | `""`                      | Use an existing PVC.                                      |
| `secret.create`             | `true`                    | Create the Secret from `secret.*`.                        |
| `secret.vaultKey`           | `""`                      | **Required.** age key from `stella vault keygen`.         |
| `secret.existingSecret`     | `""`                      | Use an existing Secret (must hold `STELLA_VAULT_KEY`).    |
| `secret.apiKeys`            | `{}`                      | Provider keys (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, …). |
| `extraEnv`                  | `{}`                      | Extra non-secret env (e.g. `STELLA_BASE_URL`, `OTEL_*`).  |
| `ingress.enabled`           | `false`                   | Create an Ingress.                                        |
| `resources`                 | 250m/512Mi → 2/2Gi        | Requests/limits.                                          |
| `autoscaling.enabled`       | `false`                   | **Unsupported on SQLite.** Example only.                  |

See [`values.yaml`](./values.yaml) for the full set with inline docs.

### Sandbox backend

The `local` backend (bubblewrap) needs `unshare(2)`, which the default
`RuntimeDefault` seccomp profile blocks — the chart sets the container
`seccompProfile` to `Unconfined` (mirroring the Docker
`--security-opt seccomp=unconfined`). If your policy forbids that, set
`sandbox.backend=none` and restore a stricter `securityContext.seccompProfile`,
but note agent tools then run without isolation. The `docker` backend needs a
host Docker socket and is not wired by this chart.

### Health probes

Liveness, readiness, and startup probes hit `GET /api/status` (unauthenticated,
returns 200 when the server is up). The startup probe absorbs slow first-boot DB
migration before liveness begins.

### Upgrade & rollback

```bash
helm upgrade stella ./deploy/helm/stella -n stella -f my-values.yaml
helm rollback stella -n stella          # to previous revision
```

The StatefulSet uses `RollingUpdate`; with one replica the pod is recreated in
place. Back up the volume before upgrades:

```bash
kubectl -n stella exec statefulset/stella -- \
  sqlite3 /home/stella/.stella/stella.db ".backup /home/stella/.stella/backup.db"
```

## PostgreSQL & S3

Not yet supported by Stella — these backends are tracked in
[#477](https://github.com/CherryHQ/stella/issues/477). When they land, this
chart will gain `database.*` and `storage.*` values and multi-replica/HPA
support.
