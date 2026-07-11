---
title: Kubernetes
description: 使用生产级 Helm chart 在 Kubernetes 上部署 Stella。
---

Stella 在 `deploy/helm/stella` 提供了一个 Helm chart，用于在 Kubernetes 上运行单个
生产实例。它刻意做了强约束：单副本、外部 PostgreSQL、给 `STELLA_HOME` 挂持久卷、
健康探针，以及两阶段优雅摘流。**不支持多副本** —— 参见
[为什么只能单副本？](#why-only-one-replica)。

## 前置条件

- 一个 Kubernetes 集群（1.23+）和 `helm` 3。
- **外部 PostgreSQL。** 容器内 Stella 拒绝使用内嵌数据库。你需要一台可达的
  PostgreSQL，装有 `pgvector` 与 `pg_search` 扩展，并拿到连接串（DSN）。
- **一个 vault key。** 用 `stellad vault keygen`（或 `age-keygen`）生成。它加密密钥、
  OAuth token 和 bearer token；每次重启都必须使用同一个 key。
- **一个 ingress**（或其他暴露 Service 的方式）以及客户端将访问的公网 URL。
- 可选：一个 S3 兼容存储桶用于持久化用户附件镜像，以及一个用于登录的 OIDC provider。
  两者都通过 `extraEnv` 传入（见下文）。

### 创建 Secret

chart 从不创建或渲染任何密钥内容，只引用你自己创建的 Secret。用你的真实值创建它：

```bash
kubectl create secret generic stella-secrets \
  --from-literal=STELLA_VAULT_KEY='AGE-SECRET-KEY-REPLACE-ME' \
  --from-literal=STELLA_DATABASE_URL='postgres://user:pass@postgres.example.com:5432/stella?sslmode=require'
```

Secret 里默认的 key 名（`STELLA_VAULT_KEY`、`STELLA_DATABASE_URL`）与 Stella 读取的
环境变量一致，无需额外映射。若必须用不同的 key 名，设置 `secrets.keys.vaultKey`
和 `secrets.keys.databaseURL`。

> 更新这个 Secret **不会**自动重启 Pod。改完之后需手动滚动：
> `kubectl rollout restart deployment/stella`。

## 安装

在仓库检出目录下：

```bash
helm install stella ./deploy/helm/stella \
  --namespace stella --create-namespace \
  --set baseURL=https://stella.example.com \
  --set secrets.existingSecret=stella-secrets \
  --set sandbox.backend=none \
  --set sandbox.allowUnsafeHostExecution=true
```

`baseURL`、`secrets.existingSecret`、`sandbox.backend` 为必填 —— 缺任意一个 chart 都
拒绝渲染。`baseURL` 必须是外部可达的 URL（ingress 地址），绝不能是 loopback：它是
OAuth 回调 URL 和通道 deep link 的来源。

用 ingress 暴露 Service：

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

不用 ingress 时，用 port-forward 访问 Web UI：

```bash
kubectl -n stella port-forward svc/stella 25678:25678
# 然后打开 http://localhost:25678
```

超过几个 flag 的配置，建议写进 `values.yaml`，用 `-f values.yaml` 安装。

## Values 参考

| 键                                       | 默认值                          | 说明                                                          |
| ---------------------------------------- | ------------------------------- | ------------------------------------------------------------- |
| `replicaCount`                           | `1`                             | 必须为 `1`，其他值一律拒绝。                                  |
| `image.repository`                       | `ghcr.io/cherryhq/stella`       | 容器镜像。                                                    |
| `image.tag`                              | `""`                            | 为空时用 chart 的 `appVersion`。                              |
| `image.pullPolicy`                       | `IfNotPresent`                  |                                                               |
| `imagePullSecrets`                       | `[]`                            | 私有 registry 用。                                            |
| `baseURL`                                | `""`                            | **必填。** 公网 URL（`STELLA_BASE_URL`）。                    |
| `secrets.existingSecret`                 | `""`                            | **必填。** 含 vault key 与 DSN 的 Secret 名。                 |
| `secrets.keys.vaultKey`                  | `STELLA_VAULT_KEY`              | Secret 中存 vault key 的键名。                                |
| `secrets.keys.databaseURL`               | `STELLA_DATABASE_URL`           | Secret 中存 DSN 的键名。                                      |
| `sandbox.backend`                        | `""`                            | **必填。** `local` 或 `none`（见 [沙箱](#sandbox-backend)）。 |
| `sandbox.allowUnsafeHostExecution`       | `false`                         | `backend=none` 时必须为 `true`。                              |
| `shutdown.preStopSeconds`                | `10`                            | preStop sleep，等待 endpoint 摘除传播。                       |
| `shutdown.httpSeconds`                   | `60`                            | `STELLA_HTTP_SHUTDOWN_TIMEOUT`。                              |
| `shutdown.riverSoftStopSeconds`          | `120`                           | `STELLA_RIVER_SOFT_STOP_TIMEOUT`。                            |
| `shutdown.terminationGracePeriodSeconds` | `200`                           | Pod grace period（见 [公式](#graceful-shutdown)）。           |
| `persistence.enabled`                    | `true`                          | 在 `/home/stella/.stella` 挂 PVC。                            |
| `persistence.size`                       | `10Gi`                          | 申请卷大小。                                                  |
| `persistence.accessMode`                 | `ReadWriteOnce`                 |                                                               |
| `persistence.storageClass`               | `""`                            | 空 = 集群默认；`-` 关闭动态供给。                             |
| `persistence.existingClaim`              | `""`                            | 复用你自己管理的 PVC，不由 chart 创建。                       |
| `service.type` / `service.port`          | `ClusterIP` / `25678`           |                                                               |
| `ingress.*`                              | 关闭                            | `className`、`annotations`、`hosts`、`tls`。                  |
| `resources`                              | requests 500m/1Gi，limits 2/4Gi |                                                               |
| `extraEnv`                               | `[]`                            | 透传普通环境变量（S3、OIDC、provider key）。                  |
| `serviceAccount.*`                       | `create: true`                  | token automount 始终关闭。                                    |

### 通过 `extraEnv` 接入可选集成

chart 刻意保持 values 面最小；可选集成走 `extraEnv`（`name`/`value` 列表），密钥类
值放进你引用的 Secret。例如 S3 附件镜像和 OIDC 登录：

```yaml
extraEnv:
  - name: STELLA_BLOB_S3_ENDPOINT
    value: https://s3.example.com
  - name: STELLA_BLOB_S3_BUCKET
    value: stella-assets
  - name: OIDC_ISSUER_URL
    value: https://id.example.com
```

凭据（S3 secret key、OIDC client secret）不要写进落在 `values.yaml` 里的 `extraEnv`
值 —— 放进 Secret 并在那里引用。

## 沙箱后端 {#sandbox-backend}

`sandbox.backend` 必填且无默认值。本 chart 支持两种后端；`docker` 后端刻意不支持
（它需要挂载 Docker socket）。

- **`none`** —— `none does not disable agent tools — it runs them directly inside
the Stella pod without sandbox isolation.`（none 不会禁用 agent 工具，而是让它们
  直接在 Stella pod 内、无沙箱隔离地运行。）因为没有隔离宿主，你必须同时设置
  `sandbox.allowUnsafeHostExecution=true` 来确认，否则渲染失败。这是 Kubernetes 上
  最简单、最可靠的后端。
- **`local`** —— bubblewrap 隔离。**在 Kubernetes 上属实验性。** 它依赖集群允许
  _非特权 user namespace_（bubblewrap 会调用 `unshare(2)`）。chart 不加任何特权
  securityContext，也不挂 Docker socket。若工具报 `bwrap:` / `unshare` /
  “Operation not permitted” 错误，要么切到 `none`（并确认
  `allowUnsafeHostExecution=true`），要么把集群/节点改成允许非特权 user namespace
  和不受限的 seccomp profile。

## 为什么只能单副本？ {#why-only-one-replica}

chart 写死了 `replicaCount: 1` 与 `strategy: Recreate`。Stella 不是无状态的：一次
多副本就绪度审计发现三类状态，一旦跑起第二个 pod 立刻出错。

- **IM 通道在每个副本上全量启动。** 每个 pod 各自启动所有已配置的
  Telegram/QQ/Feishu/微信连接。两个 pod 就是同一个 bot token 上两条 long-poll 或
  WebSocket 会话 —— 平台会返回冲突（如 Telegram `409`）或把每条消息投递两次。
- **实时推送与 per-session 串行化是进程内存态。** “同一 session 只有一个进行中
  turn” 的保证，以及把 turn 输出推给浏览器的 SSE hub，都只存在于跑该 turn 的那个
  pod 的内存里。第二个 pod 会对同一 session 并发跑 turn，且无法推送它没在跑的 turn。
- **`STELLA_HOME` 被当作持久的、pod 本地的数据。** 用户工作区、上传与通道附件、
  Recally 文章正文都写在 `STELLA_HOME` 文件里（数据库只存相对路径）。带独立卷的
  第二个 pod 看不到这些文件。

`Recreate` 保证新 pod 启动前旧 pod 已完全消失，所以这些永不重叠。chart 不提供
HPA、PodDisruptionBudget 或 NetworkPolicy 资源 —— 它们只对无状态多副本负载才有意义。

## 优雅停机 {#graceful-shutdown}

收到 `SIGTERM` 后 Stella 走两阶段摘流：`/readyz` 翻成 `503`，在
`STELLA_HTTP_SHUTDOWN_TIMEOUT` 内排空在途 HTTP 请求，再在
`STELLA_RIVER_SOFT_STOP_TIMEOUT` 内排空后台任务。chart 从 `shutdown.*` values
渲染这些值，并校验 grace period 覆盖整个摘流：

```
terminationGracePeriodSeconds >=
    preStopSeconds            (endpoint 摘除传播)
  + httpSeconds               (STELLA_HTTP_SHUTDOWN_TIMEOUT)
  + riverSoftStopSeconds      (STELLA_RIVER_SOFT_STOP_TIMEOUT)
  + 10                        (清理余量)
```

按默认值即 `10 + 60 + 120 + 10 = 200`。更小的 grace period 会在渲染期被拒 ——
否则 kubelet 会在摘流中途 `SIGKILL` 掉 pod。

`preStop` 步骤就是一个普通 `sleep`。preStop 期间应用**还没**收到 `SIGTERM`；流量
之所以被摘掉，是因为终止开始时 kubelet 会把 pod 从 Service endpoints 里移除，sleep
给这个移除留出传播时间。它**不**依赖 `/readyz` 翻成 `503`。

## 升级

由于 `strategy: Recreate`，每次升级都有一小段**停机**窗口 —— 旧 pod 停止、新 pod
启动并通过 startup probe 期间。

- **先备份。** 升级前对 PostgreSQL 做快照（`pg_dump`）并备份 `STELLA_HOME` 的 PVC。
  Stella 启动时会自动应用待执行的数据库迁移。
- **`helm rollback` 只回滚 workload，不回滚数据库。** 把 chart 回滚到旧镜像不会撤销
  新版本已经应用的迁移。若必须跨迁移回滚，请一并从备份恢复数据库。

## 存储

`STELLA_HOME`（`/home/stella/.stella`）由 PVC 支撑。chart 创建它时会带上
`helm.sh/resource-policy: keep`，所以 `helm uninstall` 会保留卷 —— 以及你的数据。
确实要删时手动删：

```bash
kubectl -n stella delete pvc stella-data
```

Pod 以 UID/GID `1000` 运行，配 `fsGroup: 1000` 和
`fsGroupChangePolicy: OnRootMismatch`，首次挂载时卷会被正确地按组授权。

## 网络出站

Stella 需要出站访问：

- **PostgreSQL** —— 你的外部数据库（通常 `5432`）。
- **LLM provider API** —— Anthropic、OpenAI，或你配置的任意 provider。
- **IM 平台 API** —— 你启用的通道对应的 Telegram、QQ、Feishu、微信。
- **S3 / 对象存储** —— 仅当你配置了 `STELLA_BLOB_S3_*` 附件镜像时。

若集群限制出站，请放行这些目标。chart 不提供 NetworkPolicy。

## 排障

- **Pod 一直不 ready / `/readyz` 返回 `503`。** 启动未完成、正在停机、或数据库 ping
  失败时 `/readyz` 都是 `503`。查 pod 日志，确认 `STELLA_DATABASE_URL` 指向一台
  可达、装有 `pgvector` 与 `pg_search` 的 PostgreSQL。startup probe 给迁移、seed、
  插件 reconcile 最多五分钟，之后 liveness/readiness 才开始。
- **写 `/home/stella/.stella` 时 `permission denied`。** 卷没有按 GID `1000` 授组。
  chart 设了 `fsGroup: 1000`；若你的 CSI 驱动忽略 `fsGroup`（有些会），请在外部给卷
  设好属主，或选一个驱动尊重 `fsGroup` 的 StorageClass。
- **agent 工具报 `bwrap` / `unshare` 错误。** 你在一个禁止非特权 user namespace 的
  集群上用 `sandbox.backend=local`。切到 `sandbox.backend=none`（并设
  `sandbox.allowUnsafeHostExecution=true`），或把节点改成允许它们。
- **OAuth/通道链接指向 localhost。** 上游的 `baseURL` 错了或没设 —— 把它设成公网
  ingress URL。
