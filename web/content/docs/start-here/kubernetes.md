---
title: Kubernetes
---

Deploy Stella on Kubernetes for production using the official Helm chart in
[`deploy/helm/stella`](https://github.com/CherryHQ/stella/tree/main/deploy/helm/stella).
The chart runs `stellad` as a single-replica StatefulSet backed by SQLite on a
persistent volume, with health probes, resource limits, and ingress wiring.

## Before you start

Stella keeps all state in one SQLite database. This deployment runs **exactly
one replica** — running more than one corrupts the database. Horizontal scaling
(multiple replicas, autoscaling) and external PostgreSQL / S3 storage are not
available yet; they are tracked in [issue #477](https://github.com/CherryHQ/stella/issues/477).
For most teams a single well-resourced replica is plenty.

You need:

- A Kubernetes cluster (1.25+) and `kubectl` access.
- [Helm](https://helm.sh/) 3.8+.
- A StorageClass that provides `ReadWriteOnce` volumes. Avoid NFS — Stella's
  database is unreliable on it.
- A vault key, generated with `stella vault keygen`. Without it, secrets, OAuth,
  and plugin credentials won't work.

## Install

Clone the repository (or copy the `deploy/helm/stella` directory), then install:

```bash
helm install stella ./deploy/helm/stella \
  --namespace stella --create-namespace \
  --set-file secret.vaultKey=/dev/stdin <<<"$(stella vault keygen)" \
  --set secret.apiKeys.ANTHROPIC_API_KEY=sk-ant-...
```

`--set-file secret.vaultKey=/dev/stdin` reads the key from the piped
`stella vault keygen` output so it never lands in your shell history or a file.
At least one provider key (for example `ANTHROPIC_API_KEY` or `OPENAI_API_KEY`)
lets agents run immediately; you can also add keys later from the Web UI.

Wait for the pod and run the bundled smoke test:

```bash
kubectl -n stella rollout status statefulset/stella
helm test stella -n stella
```

## Reach the Web UI

Port-forward for a quick check:

```bash
kubectl -n stella port-forward svc/stella 25678:25678
```

Then open `http://localhost:25678` to configure providers, channels, and agents.

For permanent external access, enable the ingress in a values file:

```yaml
# values.yaml
ingress:
  enabled: true
  className: nginx
  hosts:
    - host: stella.example.com
      paths:
        - path: /
          pathType: Prefix
  tls:
    - secretName: stella-tls
      hosts:
        - stella.example.com

# Tell Stella its public URL so Web UI links and OAuth callbacks are correct.
extraEnv:
  STELLA_BASE_URL: https://stella.example.com
```

Apply it with `helm upgrade stella ./deploy/helm/stella -n stella -f values.yaml`.

## Configure

All settings live in [`values.yaml`](https://github.com/CherryHQ/stella/blob/main/deploy/helm/stella/values.yaml)
with inline documentation. The ones you'll touch most:

| Setting                                         | Purpose                                                          |
| ----------------------------------------------- | ---------------------------------------------------------------- |
| `secret.vaultKey`                               | The vault key (required).                                        |
| `secret.apiKeys`                                | Provider API keys injected as environment variables.             |
| `secret.existingSecret`                         | Use a Secret you manage instead of letting the chart create one. |
| `persistence.size` / `persistence.storageClass` | Size and class of the data volume.                               |
| `resources`                                     | CPU/memory requests and limits.                                  |
| `sandbox.backend`                               | How agent tools are isolated — `local` or `none`.                |
| `extraEnv`                                      | Extra environment variables such as `STELLA_BASE_URL`.           |

### Health probes

Liveness, readiness, and startup probes all call `GET /api/status`, which
returns 200 once the server is up. The startup probe gives the server time to
run its first-boot database migration before the liveness probe takes over. Tune
the thresholds under `startupProbe`, `livenessProbe`, and `readinessProbe`.

### Resource limits

Defaults request 250m CPU / 512Mi memory and cap at 2 CPU / 2Gi. Adjust
`resources` to match your workload — agents running tools are the main driver.

### Sandbox isolation

The default `local` backend isolates agent tool execution and needs a relaxed
seccomp profile to do so; the chart sets this for you. If your cluster policy
forbids relaxed seccomp, set `sandbox.backend: none` — agent tools then run
without isolation, so use it only for trusted workloads.

### Using an externally managed secret

To manage the vault key and provider keys yourself, create a Secret containing
`STELLA_VAULT_KEY` (and any provider keys), then point the chart at it:

```bash
kubectl -n stella create secret generic stella-secrets \
  --from-literal=STELLA_VAULT_KEY="$(stella vault keygen)" \
  --from-literal=ANTHROPIC_API_KEY=sk-ant-...

helm install stella ./deploy/helm/stella -n stella \
  --set secret.create=false --set secret.existingSecret=stella-secrets
```

## Back up your data

The data volume holds your database, secrets, skills, and user files — back it up
before upgrades:

```bash
kubectl -n stella exec statefulset/stella -- \
  sqlite3 /home/stella/.stella/stella.db ".backup /home/stella/.stella/backup.db"
kubectl -n stella cp stella-0:/home/stella/.stella/backup.db ./stella-backup.db
```

## Upgrade and roll back

```bash
helm upgrade stella ./deploy/helm/stella -n stella -f values.yaml
helm rollback stella -n stella          # revert to the previous release
```

With a single replica the pod is recreated in place during an upgrade, so expect
a short downtime window. Changes to configuration or secrets automatically roll
the pod.

## Troubleshooting

- **Pod stuck `Pending`.** No volume could be bound — check that your cluster has
  a default StorageClass offering `ReadWriteOnce` (`kubectl get storageclass`).
- **Pod crash-loops at startup.** Usually a missing or malformed vault key.
  Confirm the Secret has `STELLA_VAULT_KEY`: `kubectl -n stella get secret stella -o yaml`.
- **Readiness never passes.** Check logs with `kubectl -n stella logs statefulset/stella`.
  The first boot runs migrations; if it takes longer than the startup probe
  allows, raise `startupProbe.failureThreshold`.
- **Agent tools fail to run.** If your cluster blocks the relaxed seccomp profile
  the `local` sandbox needs, switch to `sandbox.backend: none` for trusted
  workloads.

## Don't scale this

Do not raise `replicaCount` or set `autoscaling.enabled=true` on SQLite — the
chart guards against the latter, but both lead to a corrupted database. Wait for
the PostgreSQL backend before scaling horizontally.
