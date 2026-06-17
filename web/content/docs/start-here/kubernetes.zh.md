---
title: Kubernetes
---

使用官方 Helm Chart（位于 [`deploy/helm/stella`](https://github.com/CherryHQ/stella/tree/main/deploy/helm/stella)）在 Kubernetes 上以生产方式部署 Stella。该 Chart 以单副本 StatefulSet 运行 `stellad`，由持久卷上的 SQLite 提供存储，并配置了健康探针、资源限制和 Ingress。

## 开始之前

Stella 将全部状态保存在单个 SQLite 数据库中。本部署**只运行一个副本**——运行多个副本会损坏数据库。横向扩缩（多副本、自动扩缩）以及外部 PostgreSQL / S3 存储尚不可用，相关进展见 [issue #477](https://github.com/CherryHQ/stella/issues/477)。对大多数团队来说，一个资源充足的副本已足够。

你需要：

- 一个 Kubernetes 集群（1.25+）以及 `kubectl` 访问权限。
- [Helm](https://helm.sh/) 3.8+。
- 提供 `ReadWriteOnce` 卷的 StorageClass。请避免使用 NFS——Stella 的数据库在其上不可靠。
- 一个用 `stella vault keygen` 生成的密钥库密钥。没有它，密钥、OAuth 和插件凭证都无法工作。

## 安装

克隆仓库（或复制 `deploy/helm/stella` 目录），然后安装：

```bash
helm install stella ./deploy/helm/stella \
  --namespace stella --create-namespace \
  --set-file secret.vaultKey=/dev/stdin <<<"$(stella vault keygen)" \
  --set secret.apiKeys.ANTHROPIC_API_KEY=sk-ant-...
```

`--set-file secret.vaultKey=/dev/stdin` 从管道传入的 `stella vault keygen` 输出读取密钥，因此它不会留在 shell 历史或文件中。提供至少一个模型供应商密钥（例如 `ANTHROPIC_API_KEY` 或 `OPENAI_API_KEY`）可让智能体立即运行；你也可以稍后在 Web UI 中添加密钥。

等待 Pod 就绪并运行内置冒烟测试：

```bash
kubectl -n stella rollout status statefulset/stella
helm test stella -n stella
```

## 访问 Web UI

快速检查可使用端口转发：

```bash
kubectl -n stella port-forward svc/stella 25678:25678
```

然后打开 `http://localhost:25678` 配置供应商、渠道和智能体。

如需持久的外部访问，在 values 文件中启用 Ingress：

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

# 告知 Stella 其公开 URL，使 Web UI 链接和 OAuth 回调正确。
extraEnv:
  STELLA_BASE_URL: https://stella.example.com
```

用 `helm upgrade stella ./deploy/helm/stella -n stella -f values.yaml` 应用。

## 配置

所有配置都在 [`values.yaml`](https://github.com/CherryHQ/stella/blob/main/deploy/helm/stella/values.yaml) 中，并附有行内说明。最常用的几项：

| 配置项                                          | 用途                                         |
| ----------------------------------------------- | -------------------------------------------- |
| `secret.vaultKey`                               | 密钥库密钥（必填）。                         |
| `secret.apiKeys`                                | 作为环境变量注入的供应商 API 密钥。          |
| `secret.existingSecret`                         | 使用你自行管理的 Secret，而非让 Chart 创建。 |
| `persistence.size` / `persistence.storageClass` | 数据卷的大小与 StorageClass。                |
| `resources`                                     | CPU/内存的请求与限制。                       |
| `sandbox.backend`                               | 智能体工具的隔离方式——`local` 或 `none`。    |
| `extraEnv`                                      | 额外的环境变量，例如 `STELLA_BASE_URL`。     |

### 健康探针

存活、就绪和启动探针都调用 `GET /api/status`，服务启动后返回 200。启动探针给服务留出首次启动迁移数据库的时间，之后再由存活探针接管。可在 `startupProbe`、`livenessProbe`、`readinessProbe` 下调整阈值。

### 资源限制

默认请求 250m CPU / 512Mi 内存，上限为 2 CPU / 2Gi。根据负载调整 `resources`——运行工具的智能体是主要消耗来源。

### 沙箱隔离

默认的 `local` 后端会隔离智能体工具执行，为此需要放宽的 seccomp 配置，Chart 已为你设置。如果集群策略禁止放宽 seccomp，可设置 `sandbox.backend: none`——此时智能体工具不再隔离，仅用于可信负载。

### 使用自行管理的 Secret

若想自行管理密钥库密钥与供应商密钥，先创建包含 `STELLA_VAULT_KEY`（及任意供应商密钥）的 Secret，再让 Chart 指向它：

```bash
kubectl -n stella create secret generic stella-secrets \
  --from-literal=STELLA_VAULT_KEY="$(stella vault keygen)" \
  --from-literal=ANTHROPIC_API_KEY=sk-ant-...

helm install stella ./deploy/helm/stella -n stella \
  --set secret.create=false --set secret.existingSecret=stella-secrets
```

## 备份数据

数据卷保存着数据库、密钥、技能和用户文件——升级前请务必备份：

```bash
kubectl -n stella exec statefulset/stella -- \
  sqlite3 /home/stella/.stella/stella.db ".backup /home/stella/.stella/backup.db"
kubectl -n stella cp stella-0:/home/stella/.stella/backup.db ./stella-backup.db
```

## 升级与回滚

```bash
helm upgrade stella ./deploy/helm/stella -n stella -f values.yaml
helm rollback stella -n stella          # 回退到上一个版本
```

单副本下，升级时 Pod 会原地重建，因此会有短暂的停机窗口。配置或密钥变更会自动触发 Pod 滚动更新。

## 故障排查

- **Pod 一直处于 `Pending`。** 没有可绑定的卷——检查集群是否有提供 `ReadWriteOnce` 的默认 StorageClass（`kubectl get storageclass`）。
- **Pod 启动时反复崩溃。** 通常是密钥库密钥缺失或格式错误。确认 Secret 中包含 `STELLA_VAULT_KEY`：`kubectl -n stella get secret stella -o yaml`。
- **就绪探针始终不通过。** 用 `kubectl -n stella logs statefulset/stella` 查看日志。首次启动会运行迁移；若耗时超过启动探针允许的时间，请调高 `startupProbe.failureThreshold`。
- **智能体工具无法运行。** 如果集群禁止 `local` 沙箱所需的放宽 seccomp 配置，可对可信负载改用 `sandbox.backend: none`。

## 不要扩缩

在 SQLite 下不要提高 `replicaCount` 或设置 `autoscaling.enabled=true`——Chart 会拦截后者，但两者都会导致数据库损坏。请等待 PostgreSQL 后端就绪后再做横向扩缩。
