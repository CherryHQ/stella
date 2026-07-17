---
title: 部署
---

## 安装

### Homebrew（macOS 和 Linux）

```bash
brew install CherryHQ/tap/stella
```

如果不设置 `STELLA_DATABASE_URL`，启动服务前先运行一次 `stellad postgres download-runtime`。

### Linux 软件包（.deb / .rpm）

预构建的安装包可在 [Releases](https://github.com/CherryHQ/stella/releases) 页面获取。`bubblewrap` 已声明为依赖项，会自动安装。

```bash
# Debian / Ubuntu
sudo apt install ./stella_*_linux_amd64.deb

# Fedora / RHEL
sudo dnf install ./stella_*_linux_amd64.rpm
```

如果不设置 `STELLA_DATABASE_URL`，启动服务前先运行一次 `stellad postgres download-runtime`。

### 二进制文件

从 [GitHub Releases](https://github.com/CherryHQ/stella/releases) 下载适用于 linux、macOS 或 Windows（amd64/arm64）的预编译二进制文件，然后将其放置在 `$PATH` 中。

```bash
# 示例：Linux amd64
curl -LO https://github.com/CherryHQ/stella/releases/latest/download/stella_linux_amd64.tar.gz
tar xzf stella_linux_amd64.tar.gz
chmod +x stellad
sudo mv stellad /usr/local/bin/
```

### Go

```bash
go install github.com/CherryHQ/stella/cmd/stellad@latest
# 或
git clone https://github.com/CherryHQ/stella.git
cd stella && go build -o dist/bin/stellad ./cmd/stellad/
```

## 运行

启动服务器 —— Web UI访问地址：`http://localhost:25678`：

```bash
stellad server
```

这会启动服务器并提供Web UI，你可以在其中设置 API 密钥、渠道和代理配置。所有配置都存储在 PostgreSQL 中——可以使用 `~/.stella` 下的内嵌集群，也可以在设置 `STELLA_DATABASE_URL` 时使用外部服务器。如果缺少内嵌 runtime，先运行一次 `stellad postgres download-runtime` 再启动 `stellad server`。无需手动配置文件。

```bash
stellad server --port 8080                  # 自定义端口
stellad server --host 0.0.0.0 --port 8080   # 绑定所有网络接口
```

### 版本和自动升级

```bash
stellad version
stellad upgrade
stellad upgrade 0.50.0                             # 安装指定版本
stellad upgrade --install-dir "$HOME/.local/bin"  # 自定义安装路径
```

`stellad upgrade` 从 GitHub 获取稳定版本（默认最新，也可通过参数指定版本），下载与当前操作系统/架构匹配的安装包并显示下载进度，并默认替换当前正在运行的 `stellad` 二进制文件。如果目标目录不可写，请用具备对应系统权限的用户重新运行，或使用 `--install-dir` 指定其他目录。如果二进制文件被锁定或显示 busy，请先停止正在运行的 Stella 进程或服务，再重试。

### 启用 Structured Reflect

Reflect 写入器和生命周期 curator 是两个独立的启动时开关。每次修改模式后都需要重启 Stella。

1. 先使用 `STELLA_REFLECT_MODE=legacy` 和 `STELLA_REFLECT_CURATOR_MODE=shadow`。Shadow 会扫描生产数据并记录候选数量、命中规则、耗时和错误，但不会修改 Knowledge 或 Skill。
2. 至少完成一次完整 Shadow 扫描后，将 `STELLA_REFLECT_MODE` 改为 `structured`，curator 继续保持 `shadow`。检查 Fact/Skill 两条线的进度、模型调用、写入、no-op 和失败情况。
3. 只有在经过认证的 Knowledge 和 Skill 恢复能力可用且恢复测试通过后，才将 `STELLA_REFLECT_CURATOR_MODE` 改为 `armed`。

回滚 Reflect 写入时，将 `STELLA_REFLECT_MODE` 改回 `legacy` 并重启。要单独停止 curator 生命周期写入，将 `STELLA_REFLECT_CURATOR_MODE` 改回 `shadow` 并重启；只读扫描和 telemetry 会继续运行。模式切换会保守地维护 watermark，避免落后的处理线丢失待处理对话。

默认值和可选值见[配置](/docs/start-here/configuration#环境变量)。[记忆系统内部原理](/docs/development/memory-internals#structured-reflect-与-curator-上线机制)进一步说明 watermark 切换、Shadow 证据和 fail-closed 行为。

## 作为后台服务运行

### macOS — Homebrew

```bash
brew services start stella   # 登录时启动，崩溃后自动重启
brew services stop stella
brew services restart stella
```

### macOS — 手动

```bash
stellad service install       # 安装 LaunchAgent 并启动
stellad service status
stellad service logs --follow
stellad service stop
stellad service start
stellad service restart
stellad service uninstall
```

日志写入 `~/Library/Logs/stella/stella.log`。agent 在登录时自动启动，崩溃后自动重启。

### Linux — systemd 用户模式（无需 root）

服务以当前用户身份运行，登录时自动启动。运行前需先安装 `bubblewrap`（通过 Homebrew 或包管理器安装时会自动拉取；直接使用二进制文件时请手动安装：`apt install bubblewrap` / `dnf install bubblewrap`）。

```bash
stellad service install
stellad service status
stellad service logs --follow
stellad service stop
stellad service start
stellad service restart
stellad service uninstall
```

Unit 文件安装至 `~/.config/systemd/user/stella.service`。

### Linux — systemd 系统模式（需要 root）

以 root 身份运行，开机自动启动。

```bash
sudo stellad service install --system
stellad service status
stellad service logs --follow
sudo stellad service uninstall --system
```

Unit 文件安装至 `/etc/systemd/system/stella.service`。

## Docker

镜像发布到 `ghcr.io/cherryhq/stella`，支持 `linux/amd64` 和 `linux/arm64` 平台。

### 标签

| 标签           | 描述         |
| -------------- | ------------ |
| `latest`       | 最新稳定版本 |
| `v1.2.3`       | 特定版本     |
| `sha-<commit>` | 特定提交     |

### 快速开始

Docker 镜像要求使用外部 PostgreSQL 18，并且该数据库已安装 `pg_search` 和 `pgvector`。必须设置 `STELLA_DATABASE_URL`；内嵌 runtime 下载路径只用于非 Docker 安装。

首先，使用 `--port 8080` 运行 `stellad server`，通过Web UI进行配置：

```bash
docker run -it --rm \
  --security-opt seccomp=unconfined \
  -v ~/.stella:/home/stella/.stella \
  -p 8080:8080 \
  -e STELLA_DATABASE_URL='postgres://user:pass@postgres.example.com:5432/stella?sslmode=require' \
  ghcr.io/cherryhq/stella:latest \
  stellad server --port 8080
```

然后启动服务器：

```bash
docker run -d \
  --name stella \
  --security-opt seccomp=unconfined \
  -v ~/.stella:/home/stella/.stella \
  -p 25678:25678 \
  -e STELLA_DATABASE_URL='postgres://user:pass@postgres.example.com:5432/stella?sslmode=require' \
  -e ANTHROPIC_API_KEY=sk-... \
  ghcr.io/cherryhq/stella:latest \
  stellad server
```

容器以 `nonroot` 用户运行。挂载 `~/.stella` 以持久化技能和缓存；PostgreSQL 数据保存在外部数据库中。你可以设置 `STELLA_HOME` 来更改容器内的数据目录。`--security-opt seccomp=unconfined` 标志是本地沙箱后端（bwrap）在容器内调用 `unshare(2)` 所必需的。

### Docker Compose

```yaml
# docker-compose.yml
services:
  stella:
    image: ghcr.io/cherryhq/stella:latest
    restart: unless-stopped
    security_opt:
      - seccomp=unconfined
    volumes:
      - ./stella-data:/home/stella/.stella
    environment:
      - STELLA_DATABASE_URL=postgres://user:pass@postgres.example.com:5432/stella?sslmode=require
      - ANTHROPIC_API_KEY=sk-...
      # - OPENAI_API_KEY=sk-...
```

`seccomp=unconfined` 标志是 `local` 沙箱后端（bubblewrap）所必需的。如果 agent 使用 `docker` 沙箱后端，需要额外挂载 Docker socket 和设置模式相关的环境变量——请参阅[沙箱指南](/docs/guides/sandbox#docker-compose-示例)了解所有 compose 变体。

```bash
docker compose up -d
```

要运行初始设置，使用 `--port 8080` 启动服务器，通过 `http://localhost:8080` 的Web UI进行配置，或使用 `docker compose exec stella stellad server --port 8080`。Compose 服务必须设置 `STELLA_DATABASE_URL`。

### 本地构建

```bash
# 单平台
docker build -t stella .

# 多平台
docker buildx build --platform linux/amd64,linux/arm64 -t stella .
```

## 受管部署

当你在编排系统（Kubernetes 等）下运行 Stella 时，两个本地环境下的便利做法会变成陷阱：内嵌的单节点数据库，以及指向 pod 自身的 base URL。Docker 镜像默认拒绝前者（`STELLA_REQUIRE_EXTERNAL_DB=1`），对后者 Stella 会发出响亮的警告。

在 Kubernetes 上部署请使用生产级 Helm chart，完整步骤见 [Kubernetes 部署指南](/docs/admin/kubernetes)；本节其余内容解释 chart 已为你配置好的那些概念。

### 三种 URL 角色

Stella 使用三个不同的地址，务必区分：

| 变量                | 角色                                                                                                         |
| ------------------- | ------------------------------------------------------------------------------------------------------------ |
| `HOST`              | 服务绑定的网卡地址。容器内使用 `0.0.0.0` 让 pod 可达；默认 `127.0.0.1`。                                     |
| `STELLA_SERVER_URL` | CLI 与进程内调用方访问本地服务所用的地址。pod 内部使用；默认 `http://127.0.0.1:25678`。                      |
| `STELLA_BASE_URL`   | 客户端看到的公网 canonical URL。它是 OAuth 回调地址与频道外链的来源，因此必须是外部可达地址，而非 loopback。 |

绑定到 `0.0.0.0`（`HOST`）**并不会**给你一个公网 URL：`STELLA_BASE_URL` 未设置时，base URL 由绑定地址推导，仍然解析为 loopback。受管部署中请始终显式设置 `STELLA_BASE_URL`。

### 外部数据库要求

Docker 镜像设置了 `STELLA_REQUIRE_EXTERNAL_DB=1`：当 `STELLA_DATABASE_URL` 未设置时，启动会以可操作的错误快速失败，而不是在容器的临时文件系统上静默启动内嵌 PostgreSQL 集群——多副本时每个 pod 甚至会各建一套数据库。请将 `STELLA_DATABASE_URL` 指向带 `pgvector` 与 `pg_search` 的外部 PostgreSQL。若要有意在挂载持久卷的单容器中运行内嵌 PostgreSQL，设置 `STELLA_REQUIRE_EXTERNAL_DB=0`。

上传的用户资产同样需要持久化。未配置 `STELLA_BLOB_S3_*` 时，`STELLA_HOME` 下的文件系统是单节点权威，必须挂载持久卷；配置 S3 兼容对象存储后，它成为共享权威，本地文件只作为 materialization。Stella 当前只开放单副本 Helm 拓扑；未来唯一的多副本拓扑会直接要求共享权威，而不是再引入一个可能与实际存储冲突的模式开关。

loopback base URL 永远不是启动错误——通过 `localhost` 或 `kubectl port-forward` 访问 Stella 时它是合法的——但当配置了 OAuth/OIDC 登录时 Stella 会发出响亮警告，因为登录跳转会指回 pod 自身。部署 chart 应将 `STELLA_BASE_URL` 作为必填值：那一层才知道自己位于 ingress 之后。

### 受管部署所需的环境变量

| 变量                         | 值                                                                            |
| ---------------------------- | ----------------------------------------------------------------------------- |
| `STELLA_DATABASE_URL`        | 含 `pgvector` + `pg_search` 的外部 PostgreSQL DSN                             |
| `STELLA_VAULT_KEY`           | 密钥库的 age 私钥（用 `stellad vault keygen` 生成）                           |
| `STELLA_BASE_URL`            | 客户端使用的公网 canonical URL（如 `https://stella.example.com`）             |
| `STELLA_REQUIRE_EXTERNAL_DB` | `1` —— Docker 镜像已默认设置；未配外部数据库时快速失败而非启动内嵌 PostgreSQL |

完整的 Kubernetes manifest 演示不在本页范围内。

### 优雅停机与探针

Stella 暴露两个供编排器使用的免鉴权基础设施端点：

| 路径       | 含义                                                                                                                             |
| ---------- | -------------------------------------------------------------------------------------------------------------------------------- |
| `/healthz` | 存活探针（liveness）。只要进程在运行且能提供 HTTP，就返回 `200`。不依赖数据库或停机状态。                                        |
| `/readyz`  | 就绪探针（readiness）。仅当启动完成、尚未开始停机、且 `2s` 内数据库 ping 成功时返回 `200`；否则返回 `503` 并在响应体中说明原因。 |

收到第一个 `SIGTERM`/`SIGINT` 时，网关不会立即退出，而是执行**两阶段优雅排空**：

1. `/readyz` 翻转为 `503`，空闲的订阅流（SSE watch）结束，使负载均衡器停止转发新流量；承载进行中 turn 的流会继续运行至 turn 完成。
2. 在 `STELLA_HTTP_SHUTDOWN_TIMEOUT` 预算内排空进行中的 HTTP 请求；预算耗尽后仍未结束的连接被强制关闭。
3. 后台任务（goal 与 scheduler 的 agent 运行）继续执行，并在 `STELLA_RIVER_SOFT_STOP_TIMEOUT` 预算内排空；预算耗尽时仍在运行的任务被取消。

在排空期间收到**第二个**信号会立即硬停。这两个预算是**排空预算**，用于给停机设定上界；它们**不保证**任意一次长时间运行的 LLM turn 能跑完。

| 变量                             | 默认值 | 用途                                                       |
| -------------------------------- | ------ | ---------------------------------------------------------- |
| `STELLA_HTTP_SHUTDOWN_TIMEOUT`   | `60s`  | 在强制关闭连接前，排空进行中 HTTP 请求的预算。必须 `> 0`。 |
| `STELLA_RIVER_SOFT_STOP_TIMEOUT` | `120s` | 在取消任务上下文前，排空进行中后台任务的预算。必须 `> 0`。 |

两者都接受 Go duration（`60s`、`2m`、`500ms`）。无法解析或非正数的值会让启动快速失败。

信号一到，排空立即开始。在 Kubernetes 上请用 `preStop` sleep 为 endpoint 传播留出窗口：Pod 进入终止流程时 kubelet 就会将其从 endpoints 中摘除，sleep 把 `SIGTERM` 推迟到路由收敛之后。

探针与生命周期配置示例：

```yaml
readinessProbe:
  httpGet:
    path: /readyz
    port: 25678
  periodSeconds: 5
  failureThreshold: 3
livenessProbe:
  httpGet:
    path: /healthz
    port: 25678
  periodSeconds: 10
  failureThreshold: 3
lifecycle:
  preStop:
    sleep:
      seconds: 5
# 必须大于整个排空时长，避免 kubelet 在排空中途 SIGKILL：
#   terminationGracePeriodSeconds >=
#     preStop sleep                  （endpoint 传播延迟）
#   + STELLA_HTTP_SHUTDOWN_TIMEOUT   （HTTP 排空预算）
#   + STELLA_RIVER_SOFT_STOP_TIMEOUT （后台任务排空预算）
#   + 清理余量
# 默认值下（5 + 60 + 120 + 余量）取 >= 200。
terminationGracePeriodSeconds: 200
```

## 沙箱后端

将 Stella 运行在 Docker 容器中（见上文）与使用 Docker 作为 agent 工具执行的沙箱后端是两件独立的事。Stella 支持三种沙箱后端：`docker`、`local` 和 `none`。请参阅[沙箱指南](/docs/guides/sandbox)了解如何选择后端、配置 Docker 沙箱模式和排查常见问题。

## 卷和数据

所有数据都存储在 stella 主目录下（默认为 `~/.stella`，可通过 `STELLA_HOME` 配置）。

| 路径                                  | 用途                                                                            |
| ------------------------------------- | ------------------------------------------------------------------------------- |
| `~/.stella/postgres/`                 | 内嵌 PostgreSQL 数据（配置、记忆、调度器）；使用 `STELLA_DATABASE_URL` 时不存在 |
| `~/.stella/pg-runtime/`               | 下载的内嵌 PostgreSQL runtime；可用 `stellad postgres download-runtime` 重建    |
| `~/.stella/agents/{agent-id}/skills/` | 每个 agent 安装的技能                                                           |
| `~/.stella/agents/{agent-id}/SOUL.md` | 可选的每个 agent 的灵魂/身份覆盖                                                |
| `~/.stella/cache/`                    | 模型缓存（可重新生成，安全删除）                                                |

PostgreSQL 数据是唯一需要备份的关键数据。它包含所有配置、消息历史、摘要和调度器任务。使用内嵌集群时，停止服务后备份 `~/.stella/postgres/` 目录；`~/.stella/pg-runtime/` 是下载的程序文件，可重新生成。使用外部服务器时，对 `STELLA_DATABASE_URL` 所指数据库执行 `pg_dump`。

关于哪些目录属于持久数据、派生缓存或临时数据——以及各自在 Kubernetes 或临时磁盘上所需的卷与备份处理方式——完整说明参见[存储与持久化](/docs/start-here/storage)。

## 环境变量

配置通过Web UI管理（默认 `http://localhost:25678`；使用 `--port` 自定义端口）。还支持使用 `HOST` 和 `PORT` 绑定服务，其余仅支持少量环境变量：

| 变量                             | 必需                      | 描述                                                                                                                   |
| -------------------------------- | ------------------------- | ---------------------------------------------------------------------------------------------------------------------- |
| `STELLA_HOME`                    | 否                        | Stella 主目录（默认 `~/.stella`）                                                                                      |
| `STELLA_DATABASE_URL`            | Docker 中必需；其他环境否 | 外部 PostgreSQL 连接 URL；Docker 之外不设置时使用 `STELLA_HOME` 下的内嵌集群                                           |
| `STELLA_BASE_URL`                | 否¶                       | OAuth 回调与频道外链使用的公网 canonical URL；未设置时由绑定地址推导（loopback）                                       |
| `STELLA_REQUIRE_EXTERNAL_DB`     | 否                        | `STELLA_DATABASE_URL` 未设置时快速失败而非启动内嵌 PostgreSQL；Docker 镜像默认设为 `1`，设 `0` 可在持久卷上运行内嵌 PG |
| `STELLA_HTTP_SHUTDOWN_TIMEOUT`   | 否                        | 优雅停机时排空进行中 HTTP 请求的预算（Go duration，默认 `60s`，`> 0`）                                                 |
| `STELLA_RIVER_SOFT_STOP_TIMEOUT` | 否                        | 优雅停机时排空进行中后台任务的预算（Go duration，默认 `120s`，`> 0`）                                                  |
| `STELLA_BLOB_S3_ENDPOINT`        | 否§                       | 持久化用户资产镜像使用的 S3 兼容 endpoint                                                                              |
| `STELLA_BLOB_S3_BUCKET`          | 否§                       | 镜像用户上传资产的 bucket                                                                                              |
| `STELLA_BLOB_S3_ACCESS_KEY`      | 否§                       | 资产镜像使用的 access key                                                                                              |
| `STELLA_BLOB_S3_SECRET_KEY`      | 否§                       | 资产镜像使用的 secret key                                                                                              |
| `STELLA_BLOB_S3_REGION`          | 否                        | 可选 S3 region                                                                                                         |
| `STELLA_BLOB_S3_USE_SSL`         | 否                        | S3 兼容存储是否使用 HTTPS；默认 `true`                                                                                 |
| `ANTHROPIC_API_KEY`              | 是\*                      | Anthropic 提供商密钥                                                                                                   |
| `OPENAI_API_KEY`                 | 是\*                      | OpenAI 提供商密钥                                                                                                      |
| `STELLA_VAULT_KEY`               | 是†                       | 密钥库使用的 age 私钥 —— 密钥管理、OAuth 和 Bearer Token 所必需                                                        |
| `STELLA_DOCKER_SANDBOX_MODE`     | 否‡                       | 仅 `docker` 沙箱后端需要：`host`、`bind` 或 `volume`                                                                   |
| `STELLA_HOME_HOST`               | 否‡                       | `STELLA_HOME` 的宿主机侧路径；仅 `STELLA_DOCKER_SANDBOX_MODE=bind` 时需要                                              |
| `STELLA_HOME_VOLUME`             | 否‡                       | `STELLA_HOME` 的 Docker named volume 名称；仅 `STELLA_DOCKER_SANDBOX_MODE=volume` 时需要                               |

\* 至少需要一个提供商密钥。API 密钥也可以通过Web UI配置。

† 未设置 `STELLA_VAULT_KEY` 时，密钥库接口返回 `503`，无法签发 OAuth Token，插件密钥也不会被注入。使用 `age-keygen` 生成密钥。

‡ 仅当 agent 使用 `docker` 沙箱后端时需要。stellad 在宿主机上运行用 `host`；stellad 在 Docker 内且使用 host bind mount 用 `bind`；stellad 在 Docker 内且使用 named volume 用 `volume`。

§ 四个必需的 S3 镜像变量必须同时设置，或全部不设置。部分设置会导致启动失败。

¶ 受管部署所必需，以及在使用 OAuth 登录或频道外链时必需。参见[受管部署](#受管部署)。

## 健康检查

网关会记录日志到标准输出。验证它是否正在运行：

```bash
# 二进制文件
stellad server  # 日志显示在终端中

# Docker
docker logs stella
```

如需通过 HTTP 检查，可访问基础设施探针（无需鉴权）：

```bash
curl -fsS http://localhost:25678/healthz   # 存活：进程在运行
curl -fsS http://localhost:25678/readyz    # 就绪：在运行、未排空、数据库可达
```

在 Kubernetes 下如何使用这两个端点，见[优雅停机与探针](#优雅停机与探针)。
