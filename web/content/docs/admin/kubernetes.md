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
- Optional: an S3-compatible object store for legacy mutable-asset migration and immutable
  session media, and an OIDC provider for login. Both are passed through `extraEnv` (see below).

### Create the Secret

The chart never creates or templates secret material; it only references a Secret
you create yourself. Create the namespace and the Secret in it (the same namespace
you install the release into):

```bash
kubectl create namespace stella
kubectl -n stella create secret generic stella-secrets \
  --from-literal=STELLA_VAULT_KEY='AGE-SECRET-KEY-REPLACE-ME' \
  --from-literal=STELLA_DATABASE_URL='postgres://user:pass@postgres.example.com:5432/stella?sslmode=require'
```

The default key names inside the Secret (`STELLA_VAULT_KEY`, `STELLA_DATABASE_URL`)
match the environment variables Stella reads, so no further mapping is needed. If
you must use different key names, set `secrets.keys.vaultKey` and
`secrets.keys.databaseURL`.

> Updating this Secret does **not** restart the pod. After you change it, roll the
> deployment: `kubectl -n stella rollout restart deployment/stella`.

## Install

From a checkout of the repository:

```bash
helm install stella ./deploy/helm/stella \
  --namespace stella \
  --set baseURL=https://stella.example.com \
  --set secrets.existingSecret=stella-secrets \
  --set sandbox.backend=local
```

`sandbox.backend=local` is the recommended choice — it isolates agent-run tools with
bubblewrap. Only fall back to `none` after reading [Sandbox backend](#sandbox-backend);
`none` runs agent code with no isolation and exposes deployment secrets to it.

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
| `image.tag`                              | `""`                            | Defaults to `latest`; pin a version tag or digest for production.  |
| `image.digest`                           | `""`                            | `sha256:…` digest; pins the image and overrides `tag`.             |
| `image.pullPolicy`                       | `IfNotPresent`                  |                                                                    |
| `imagePullSecrets`                       | `[]`                            | For a private registry.                                            |
| `baseURL`                                | `""`                            | **Required.** Public URL (`STELLA_BASE_URL`).                      |
| `secrets.existingSecret`                 | `""`                            | **Required.** Name of the Secret with the vault key and DSN.       |
| `secrets.keys.vaultKey`                  | `STELLA_VAULT_KEY`              | Key in the Secret holding the vault key.                           |
| `secrets.keys.databaseURL`               | `STELLA_DATABASE_URL`           | Key in the Secret holding the DSN.                                 |
| `sandbox.backend`                        | `""`                            | **Required.** `local` or `none` (see [Sandbox](#sandbox-backend)). |
| `sandbox.allowUnsafeHostExecution`       | `false`                         | Must be `true` when `backend=none`.                                |
| `sandbox.seccompProfile`                 | `RuntimeDefault`                | Pod seccomp profile. `Unconfined` if `local` needs it.             |
| `shutdown.preStopSeconds`                | `10`                            | preStop sleep, for endpoint propagation.                           |
| `shutdown.httpSeconds`                   | `60`                            | `STELLA_HTTP_SHUTDOWN_TIMEOUT`.                                    |
| `shutdown.riverSoftStopSeconds`          | `120`                           | `STELLA_RIVER_SOFT_STOP_TIMEOUT`.                                  |
| `shutdown.terminationGracePeriodSeconds` | `200`                           | Pod grace period (see [formula](#graceful-shutdown)).              |
| `persistence.enabled`                    | `true`                          | Mount a PVC at `/home/stella/.stella`.                             |
| `persistence.allowEphemeralDataLoss`     | `false`                         | Must be `true` to disable persistence (emptyDir loses user data).  |
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
you reference. For example, S3 configuration for legacy migration and session media, and OIDC login:

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

Variables the chart manages through typed values are **rejected** in `extraEnv`
(`STELLA_BASE_URL`, `STELLA_SANDBOX_BACKEND`, the shutdown timeouts, the vault key
and DSN, `HOST`, `PORT`, `STELLA_REQUIRE_EXTERNAL_DB`): setting them there would
silently bypass the chart's validation. The chart also sets `HOST=0.0.0.0` and
`STELLA_REQUIRE_EXTERNAL_DB=1` explicitly rather than relying on the image's ENV
defaults, so its contract holds for any `image.repository` you substitute.

## Sandbox backend

`sandbox.backend` is required and has no default. It decides how the tools an agent
runs (`bash`, file edits) are isolated from the Stella process. This chart supports
two backends; the `docker` backend is intentionally unsupported (it would need a
mounted Docker socket).

- **`local`** (recommended) — bubblewrap isolation. Tool processes run in their own
  user, PID, and mount namespaces with the Stella process's environment scrubbed, so
  they cannot read its secrets. **Experimental on Kubernetes:** it depends on the
  cluster allowing _unprivileged user namespaces_ (bubblewrap calls `unshare(2)`).
  The chart adds no privileged securityContext and mounts no Docker socket, and
  defaults the pod's seccomp profile to `RuntimeDefault`. If tools fail with `bwrap:`
  / `unshare` / "Operation not permitted" errors, first set
  `sandbox.seccompProfile=Unconfined` (some clusters' default profile blocks
  bubblewrap's namespace syscalls); if that is not enough, reconfigure the
  cluster/node to permit unprivileged user namespaces — do not reach for `none` to
  work around it on a multi-user deployment.
- **`none`** — no isolation. `none` does not disable agent tools; it runs them
  directly inside the Stella pod as the same user and in the same process namespace.

  > **`none` exposes deployment secrets to agent code.** With no process isolation,
  > a tool can read the Stella process's environment (e.g. `/proc/1/environ`) and
  > recover `STELLA_VAULT_KEY` — the master key that decrypts **every user's**
  > secrets and tokens — and `STELLA_DATABASE_URL`. `secretKeyRef` and the `extraEnv`
  > guard do not protect against this. Only choose `none` when every user who can
  > drive an agent is fully trusted (e.g. a single-operator install), never for a
  > multi-user or public deployment. Hardening the `none` backend itself is
  > tracked in [#705](https://github.com/CherryHQ/stella/issues/705).

  Because of that, `none` also requires `sandbox.allowUnsafeHostExecution=true` to
  render. The chart still drops all Linux capabilities and disallows privilege
  escalation for the container in this mode, but that does not restore the process
  isolation the secret exposure depends on.

## Why only one replica?

The chart hard-codes `replicaCount: 1` and `strategy: Recreate`. Stella is not
stateless: a multi-replica readiness audit found three classes of state that break
the moment a second pod runs.

- **IM channels start in full on every replica.** Each pod independently starts
  every configured Telegram/Discord/QQ/Feishu/WeChat connection. Two pods means two long-poll
  or WebSocket sessions on the same bot token — platforms respond with conflicts
  (e.g. Telegram `409`) or deliver each message twice.
- **Live streaming and per-session serialization are in-process.** The guarantee
  that a session has only one in-flight turn, and the SSE hub that streams turn
  output to the browser, live in the memory of the pod running the turn. A second
  pod would run concurrent turns for the same session and could not stream a turn
  it isn't running.
- **Principal and Agent Homes are durable, pod-local data.** User workspaces,
  mutable assets, and project files remain in their owning Homes. A second pod with
  its own volume would not see them.

`Recreate` guarantees a clean rollout: the old pod is fully gone before the new one
starts, so these never overlap during an upgrade. (An involuntary disruption — a node
failure or eviction — can still briefly overlap two pods; see [Storage](#storage) for
the `ReadWriteOncePod` fencing option.)

The chart ships no HPA (autoscaling a stateful single replica is unsafe) and no
PodDisruptionBudget. A single-replica PDB is a deliberate omission, not an oversight:
`minAvailable: 1` on one replica blocks every voluntary eviction, including node
drains for maintenance. If your operations require gating voluntary disruptions,
add one yourself and accept that it will hold up node drains until the pod is
manually moved.

Because the Deployment strategy is `Recreate`, an HPA or a second replica can never
be reached through values — but a raw manifest edit could. Do not add either; the
"Why only one replica?" reasons above are correctness constraints, not tuning.

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

An S3-compatible object store supports legacy mutable-asset migration and immutable
session media. It does not replace the PVC or make Principal and Agent Homes stateless.

```bash
kubectl -n stella delete pvc stella-data
```

The pod runs as UID/GID `1000` with `fsGroup: 1000` and
`fsGroupChangePolicy: OnRootMismatch`, so the volume is group-owned correctly on
first mount.

The chart-created PVC name is derived from the release/chart name. Do not change
`nameOverride`/`fullnameOverride` after install — the pod would bind a new, empty
PVC and the old one (with your data) would be left behind by the `keep` policy. Use
`persistence.existingClaim` to pin a volume you manage yourself.

To keep a node failure from ever running two pods against the volume at once, set
`persistence.accessMode: ReadWriteOncePod` on a cluster that supports it (Kubernetes
1.29+). With plain `ReadWriteOnce`, `Recreate` prevents overlap on rollout but not
during an involuntary disruption.

## Network egress

Stella needs outbound access to:

- **PostgreSQL** — your external database (usually `5432`).
- **LLM provider APIs** — Anthropic, OpenAI, or whichever providers you configure.
- **IM platform APIs** — Telegram, Discord, QQ, Feishu, WeChat, for any channels you enable.
- **S3 / object storage** — only if you configure `STELLA_BLOB_S3_*` for legacy
  migration or immutable session media.

If your cluster restricts egress, allow these destinations. The chart does not ship
a NetworkPolicy.

**Restrict egress when you run untrusted agents.** Agent tools reach the network
with the pod's full access — and with `sandbox.backend=none` they do so as arbitrary
model-driven code. At minimum, block the cloud metadata endpoint
(`169.254.169.254`), which on IMDSv1 clusters can hand out node IAM credentials, and
deny east-west traffic you do not need. A starting NetworkPolicy:

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: stella-egress
  namespace: stella
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: stella
  policyTypes: [Egress]
  egress:
    # DNS
    - to: []
      ports:
        - { protocol: UDP, port: 53 }
        - { protocol: TCP, port: 53 }
    # Everything except link-local (blocks 169.254.169.254). Tighten to your
    # PostgreSQL, provider, IM, and S3 CIDRs for a real deployment.
    - to:
        - ipBlock:
            cidr: 0.0.0.0/0
            except: ["169.254.0.0/16"]
```

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
  Reconfigure the node to allow them. Switching to `sandbox.backend=none` also makes
  the error go away, but on a multi-user deployment that trades an isolation error
  for a secret-exposure problem — see [Sandbox backend](#sandbox-backend) first.
- **OAuth/channel links point at localhost.** `baseURL` is wrong or unset upstream —
  set it to the public ingress URL.
