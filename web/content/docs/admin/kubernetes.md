---
title: Kubernetes
description: Deploy Stella on Kubernetes with the production Helm chart.
---

Stella ships a Helm chart at `deploy/helm/stella` for running a single, production
Stella instance on Kubernetes. It is deliberately opinionated: one replica, an
external PostgreSQL, a persistent volume for `STELLA_HOME`, health probes, and a
two-phase graceful drain. Multiple replicas are not supported — see
[Why only one replica?](#why-only-one-replica).

## Prerequisites

- A Kubernetes cluster (1.23+) and `helm` 3.
- **External PostgreSQL.** Stella refuses the embedded database in a container.
  You need a reachable PostgreSQL server with the `pgvector` and `pg_search`
  extensions, and a connection URL (DSN).
- **A vault key.** Generate one with `stellad vault keygen` (or `age-keygen`). It
  encrypts secrets, OAuth tokens, and bearer tokens; every restart must use the
  same key.
- **An ingress** (or another way to expose the Service) and the public URL clients
  will use.
- Optional: an S3-compatible bucket for the durable user-asset mirror, and an
  OIDC provider for login. Both are passed through `extraEnv` (see below).

### Create the Secret

The chart never creates or templates secret material; it only references a Secret
you create yourself. Create it with your real values:

```bash
kubectl create secret generic stella-secrets \
  --from-literal=STELLA_VAULT_KEY='AGE-SECRET-KEY-REPLACE-ME' \
  --from-literal=STELLA_DATABASE_URL='postgres://user:pass@postgres.example.com:5432/stella?sslmode=require'
```

The default key names inside the Secret (`STELLA_VAULT_KEY`, `STELLA_DATABASE_URL`)
match the environment variables Stella reads, so no further mapping is needed. If
you must use different key names, set `secrets.keys.vaultKey` and
`secrets.keys.databaseURL`.

> Updating this Secret does **not** restart the pod. After you change it, roll the
> deployment: `kubectl rollout restart deployment/stella`.

## Install

From a checkout of the repository:

```bash
helm install stella ./deploy/helm/stella \
  --namespace stella --create-namespace \
  --set baseURL=https://stella.example.com \
  --set secrets.existingSecret=stella-secrets \
  --set sandbox.backend=none \
  --set sandbox.allowUnsafeHostExecution=true
```

`baseURL`, `secrets.existingSecret`, and `sandbox.backend` are required — the chart
refuses to render without them. `baseURL` must be the externally reachable URL
(the ingress address), never a loopback: it is the source for OAuth callback URLs
and channel deep links.

Expose the Service with an ingress:

```bash
helm upgrade stella ./deploy/helm/stella --namespace stella --reuse-values \
  --set ingress.enabled=true \
  --set ingress.className=nginx \
  --set 'ingress.hosts[0].host=stella.example.com' \
  --set 'ingress.hosts[0].paths[0].path=/' \
  --set 'ingress.hosts[0].paths[0].pathType=Prefix' \
  --set 'ingress.tls[0].secretName=stella-tls' \
  --set 'ingress.tls[0].hosts[0]=stella.example.com'
```

Without an ingress, reach the Web UI with a port-forward:

```bash
kubectl -n stella port-forward svc/stella 25678:25678
# then open http://localhost:25678
```

For anything beyond a couple of flags, put your settings in a `values.yaml` and
install with `-f values.yaml`.

## Values reference

| Key                                      | Default                         | Description                                                        |
| ---------------------------------------- | ------------------------------- | ------------------------------------------------------------------ |
| `replicaCount`                           | `1`                             | Must be `1`. Any other value is rejected.                          |
| `image.repository`                       | `ghcr.io/cherryhq/stella`       | Container image.                                                   |
| `image.tag`                              | `""`                            | Defaults to the chart's `appVersion`.                              |
| `image.pullPolicy`                       | `IfNotPresent`                  |                                                                    |
| `imagePullSecrets`                       | `[]`                            | For a private registry.                                            |
| `baseURL`                                | `""`                            | **Required.** Public URL (`STELLA_BASE_URL`).                      |
| `secrets.existingSecret`                 | `""`                            | **Required.** Name of the Secret with the vault key and DSN.       |
| `secrets.keys.vaultKey`                  | `STELLA_VAULT_KEY`              | Key in the Secret holding the vault key.                           |
| `secrets.keys.databaseURL`               | `STELLA_DATABASE_URL`           | Key in the Secret holding the DSN.                                 |
| `sandbox.backend`                        | `""`                            | **Required.** `local` or `none` (see [Sandbox](#sandbox-backend)). |
| `sandbox.allowUnsafeHostExecution`       | `false`                         | Must be `true` when `backend=none`.                                |
| `shutdown.preStopSeconds`                | `10`                            | preStop sleep, for endpoint propagation.                           |
| `shutdown.httpSeconds`                   | `60`                            | `STELLA_HTTP_SHUTDOWN_TIMEOUT`.                                    |
| `shutdown.riverSoftStopSeconds`          | `120`                           | `STELLA_RIVER_SOFT_STOP_TIMEOUT`.                                  |
| `shutdown.terminationGracePeriodSeconds` | `200`                           | Pod grace period (see [formula](#graceful-shutdown)).              |
| `persistence.enabled`                    | `true`                          | Mount a PVC at `/home/stella/.stella`.                             |
| `persistence.size`                       | `10Gi`                          | Requested volume size.                                             |
| `persistence.accessMode`                 | `ReadWriteOnce`                 |                                                                    |
| `persistence.storageClass`               | `""`                            | Empty = cluster default; `-` disables dynamic provisioning.        |
| `persistence.existingClaim`              | `""`                            | Reuse a PVC you manage instead of creating one.                    |
| `service.type` / `service.port`          | `ClusterIP` / `25678`           |                                                                    |
| `ingress.*`                              | disabled                        | `className`, `annotations`, `hosts`, `tls`.                        |
| `resources`                              | 500m/1Gi requests, 2/4Gi limits |                                                                    |
| `extraEnv`                               | `[]`                            | Passthrough plain env vars (S3, OIDC, provider keys).              |
| `serviceAccount.*`                       | `create: true`                  | Token automount is always off.                                     |

### Optional integrations via `extraEnv`

The chart keeps its values surface small; optional integrations go through
`extraEnv` (a list of `name`/`value` entries) and, for secret values, the Secret
you reference. For example, the S3 asset mirror and OIDC login:

```yaml
extraEnv:
  - name: STELLA_BLOB_S3_ENDPOINT
    value: https://s3.example.com
  - name: STELLA_BLOB_S3_BUCKET
    value: stella-assets
  - name: OIDC_ISSUER_URL
    value: https://id.example.com
```

Keep credentials (S3 secret key, OIDC client secret) out of `extraEnv` values that
sit in your `values.yaml` — put them in the Secret and reference them there.

## Sandbox backend

`sandbox.backend` is required and has no default. This chart supports two backends;
the `docker` backend is intentionally unsupported (it would need a mounted Docker
socket).

- **`none`** — `none` does not disable agent tools — it runs them directly inside
  the Stella pod without sandbox isolation. Because there is no isolation host,
  you must also set `sandbox.allowUnsafeHostExecution=true` to acknowledge it, or
  rendering fails. This is the simplest, most reliable backend on Kubernetes.
- **`local`** — bubblewrap isolation. **Experimental on Kubernetes.** It depends on
  the cluster allowing _unprivileged user namespaces_ (bubblewrap calls
  `unshare(2)`). The chart adds no privileged securityContext and mounts no Docker
  socket. If tools fail with `bwrap:` / `unshare` / "Operation not permitted"
  errors, either switch to `none` (and confirm `allowUnsafeHostExecution=true`) or
  reconfigure the cluster/node to permit unprivileged user namespaces and an
  unrestricted seccomp profile.

## Why only one replica?

The chart hard-codes `replicaCount: 1` and `strategy: Recreate`. Stella is not
stateless: a multi-replica readiness audit found three classes of state that break
the moment a second pod runs.

- **IM channels start in full on every replica.** Each pod independently starts
  every configured Telegram/QQ/Feishu/WeChat connection. Two pods means two long-poll
  or WebSocket sessions on the same bot token — platforms respond with conflicts
  (e.g. Telegram `409`) or deliver each message twice.
- **Live streaming and per-session serialization are in-process.** The guarantee
  that a session has only one in-flight turn, and the SSE hub that streams turn
  output to the browser, live in the memory of the pod running the turn. A second
  pod would run concurrent turns for the same session and could not stream a turn
  it isn't running.
- **`STELLA_HOME` is treated as durable, pod-local data.** User workspaces,
  uploaded and channel attachments, and Recally article bodies are written to
  `STELLA_HOME` files (the database stores only relative paths). A second pod with
  its own volume would not see them.

`Recreate` guarantees the old pod is fully gone before the new one starts, so these
never overlap. The chart does not ship HPA, PodDisruptionBudget, or NetworkPolicy
resources — they only make sense for a stateless, multi-replica workload.

## Graceful shutdown

On `SIGTERM` Stella runs a two-phase drain: `/readyz` flips to `503`, in-flight HTTP
requests drain within `STELLA_HTTP_SHUTDOWN_TIMEOUT`, then background jobs drain
within `STELLA_RIVER_SOFT_STOP_TIMEOUT`. The chart wires these from the `shutdown.*`
values and validates the grace period covers the whole drain:

```
terminationGracePeriodSeconds >=
    preStopSeconds            (endpoint propagation)
  + httpSeconds               (STELLA_HTTP_SHUTDOWN_TIMEOUT)
  + riverSoftStopSeconds      (STELLA_RIVER_SOFT_STOP_TIMEOUT)
  + 10                        (cleanup margin)
```

At the defaults that is `10 + 60 + 120 + 10 = 200`. A smaller grace period is
rejected at render time — otherwise the kubelet would `SIGKILL` the pod mid-drain.

The `preStop` step is a plain `sleep`. During `preStop` the application has **not**
received `SIGTERM` yet; traffic is shed because the kubelet removes the pod from the
Service endpoints when termination begins, and the sleep gives that removal time to
propagate. It does not depend on `/readyz` flipping to `503`.

## Upgrades

Because of `strategy: Recreate`, every upgrade has a brief **downtime** window while
the old pod stops and the new one starts and passes its startup probe.

- **Back up first.** Snapshot your PostgreSQL database (`pg_dump`) and the
  `STELLA_HOME` PVC before upgrading. Stella applies pending database migrations
  automatically on startup.
- **`helm rollback` only reverts the workload, not the database.** Rolling the chart
  back to an older image does not undo migrations that the newer version already
  applied. If you must roll back across a migration, restore the database from your
  backup as well.

## Storage

`STELLA_HOME` (`/home/stella/.stella`) is backed by a PVC. When the chart creates
it, the PVC carries `helm.sh/resource-policy: keep`, so `helm uninstall` leaves the
volume — and your data — in place. Delete it deliberately when you mean to:

```bash
kubectl -n stella delete pvc stella-data
```

The pod runs as UID/GID `1000` with `fsGroup: 1000` and
`fsGroupChangePolicy: OnRootMismatch`, so the volume is group-owned correctly on
first mount.

## Network egress

Stella needs outbound access to:

- **PostgreSQL** — your external database (usually `5432`).
- **LLM provider APIs** — Anthropic, OpenAI, or whichever providers you configure.
- **IM platform APIs** — Telegram, QQ, Feishu, WeChat, for any channels you enable.
- **S3 / object storage** — only if you configure the `STELLA_BLOB_S3_*` asset mirror.

If your cluster restricts egress, allow these destinations. The chart does not ship
a NetworkPolicy.

## Troubleshooting

- **Pod never becomes ready / `/readyz` returns `503`.** `/readyz` is `503` while
  startup is incomplete, during shutdown, or when the database ping fails. Check the
  pod logs and confirm `STELLA_DATABASE_URL` points at a reachable PostgreSQL with
  `pgvector` and `pg_search`. The startup probe allows up to five minutes for
  migrations, seed, and plugin reconcile before liveness/readiness begin.
- **`permission denied` writing under `/home/stella/.stella`.** The volume was not
  group-owned for GID `1000`. The chart sets `fsGroup: 1000`; if your CSI driver
  ignores `fsGroup` (some do), set ownership on the volume out-of-band or choose a
  StorageClass whose driver honors `fsGroup`.
- **Agent tools fail with `bwrap` / `unshare` errors.** You are on
  `sandbox.backend=local` in a cluster that blocks unprivileged user namespaces.
  Switch to `sandbox.backend=none` (with `sandbox.allowUnsafeHostExecution=true`) or
  reconfigure the node to allow them.
- **OAuth/channel links point at localhost.** `baseURL` is wrong or unset upstream —
  set it to the public ingress URL.
