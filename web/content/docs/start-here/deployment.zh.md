---
title: 部署
---

## 安装

### Homebrew（macOS 和 Linux）

```bash
brew install CherryHQ/tap/stella
```

如果不设置 `STELLA_DATABASE_URL`，启动服务前先运行一次 `stellad postgres download`。

### Linux 软件包（.deb / .rpm）

预构建的安装包可在 [Releases](https://github.com/CherryHQ/stella/releases) 页面获取。`bubblewrap` 已声明为依赖项，会自动安装。

```bash
# Debian / Ubuntu
sudo apt install ./stella_*_linux_amd64.deb

# Fedora / RHEL
sudo dnf install ./stella_*_linux_amd64.rpm
```

如果不设置 `STELLA_DATABASE_URL`，启动服务前先运行一次 `stellad postgres download`。

### 二进制文件

从 [GitHub Releases](https://github.com/CherryHQ/stella/releases) 下载适用于 Linux 或 macOS（amd64/arm64）的预编译二进制文件，然后将其放置在 `$PATH` 中。

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

Stella 原生服务端支持 Linux 和 macOS。持久 Home 与可变 Skill authority 依赖 POSIX `openat`、原子 no-replace publication 和文件系统持久性语义，因此 release 不再发布 Windows 服务端二进制文件。从源码构建的 Windows 二进制会在读取配置、启动数据库或修改存储之前拒绝 `server` 和 `upgrade`。现有 Windows 部署必须先把数据库与完整的 `STELLA_HOME` 搬到 Linux 或 macOS 的持久 POSIX 存储，再执行升级。可以在 Windows 机器上的 Linux 虚拟机或容器中运行 Stella，但 `STELLA_HOME` 必须由具备这些 POSIX 语义的存储承载，不能使用 Windows 文件系统 bind mount。

Stella 在 Linux 和 macOS 上内置 Xberg 文档运行时，PDF 和 DOCX Knowledge 上传不需要额外安装系统软件包，也不会在启动后下载依赖。

启动服务器 —— Web UI访问地址：`http://localhost:25678`：

```bash
stellad server
```

这会启动服务器并提供Web UI，你可以在其中设置 API 密钥、渠道和代理配置。所有配置都存储在 PostgreSQL 中——可以使用 `~/.stella` 下的内嵌集群，也可以在设置 `STELLA_DATABASE_URL` 时使用外部服务器。如果缺少内嵌 runtime，先运行一次 `stellad postgres download` 再启动 `stellad server`。无需手动配置文件。

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

### Structured Reflect 与 Curator

Structured Reflect 始终启用，生命周期 curator 默认使用 `armed`。

部署后执行以下检查：

1. 确认一次完整 Reflect scheduler 运行推进 Fact/Skill 两条独立 watermark，并记录预期的模型调用、写入、no-op、耗时和错误。
2. 确认 curator 以 `armed` 运行，并且只处理符合条件的 Reflect-owned Knowledge 和 Skill。
3. 验证符合条件的 Knowledge 可以恢复。Curator 删除的 Reflect-owned Skill 不可恢复。
4. 如需停止后续生命周期写入，将 `STELLA_REFLECT_CURATOR_MODE=shadow` 并重启。Shadow 会继续执行确定性扫描和记录 telemetry，但不会废弃 Knowledge 或删除 Skill。

如果必须回滚整个版本，请部署上一版本二进制，而不是在新版本中选择 legacy writer。切换迁移会保留旧 global watermark，并初始化两条 structured line watermark，使上一版本能够保守地继续处理。

可选值见[配置](/docs/start-here/configuration#环境变量)。[记忆系统内部原理](/docs/development/memory-internals#structured-reflect-与-curator)进一步说明 watermark 迁移、Shadow 行为和 fail-closed wiring。

## 将 Web UI 安装为应用

Web UI 是一个渐进式 Web 应用，你可以把它安装到程序坞、任务栏或主屏幕，在独立窗口中打开，不带浏览器界面。

- **Chrome、Edge、Brave（桌面端和 Android）** — 打开 Web UI，点击地址栏中的安装图标，或选择**菜单 → 安装 Stella**。
- **iPhone 和 iPad 上的 Safari** — 打开 Web UI，选择**分享 → 添加到主屏幕**。
- **macOS 上的 Safari** — 选择**文件 → 添加到程序坞**。

安装后打开过几次，之后即使离线启动，应用也会显示界面，而不是浏览器错误页。与 agent 对话仍然需要服务端，因此界面之外的功能会等待连接恢复。

升级 Stella 后，仍然开着的应用或标签页会提示刷新以载入新版本。你可以忽略该提示继续工作，下次升级时它会再次出现。

浏览器只允许在安全来源上安装。如果你通过局域网地址以纯 `http://` 访问 Stella，不会出现安装选项，应用会保持为普通浏览器标签页。请为 Stella 配置 HTTPS（见[三种 URL 角色](#三种-url-角色)），或使用浏览器视为安全来源的 `http://localhost` 访问。

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
  ghcr.io/cherryhq/stella:latest \
  stellad server
```

容器以 `nonroot` 用户运行。挂载 `$STELLA_HOME`（通常为 `~/.stella`）以保留 Agent 工作树和 Project Skill、未镜像资产及缓存；数据库支持的可变 Skill 仍位于外部 PostgreSQL。发行版自带 builtin 来自镜像中的不可变 bundle，而不是宿主机。你可以设置 `STELLA_HOME` 来更改容器内的数据目录。`--security-opt seccomp=unconfined` 标志是本地沙箱后端（bwrap）在容器内调用 `unshare(2)` 所必需的。

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

上传的用户资产需要位于 `STELLA_HOME` 下的持久 POSIX 存储；S3 配置不会镜像或恢复这棵可变树。Stella 当前只开放单副本 Helm 拓扑；未来副本需要同一个共享、强一致 POSIX 命名空间。`STELLA_BLOB_S3_*` 是可选配置，仅服务于内容寻址 session media 等独立的 immutable BlobStore 数据。

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

1. `/readyz` 翻转为 `503`，SSE 观察者断开，使负载均衡器停止转发新流量；进行中的 turn 作为服务端 accepted work 继续运行，并在关停完成前持久化。
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

| 路径                                          | 用途                                                                            |
| --------------------------------------------- | ------------------------------------------------------------------------------- |
| `~/.stella/postgres/`                         | 内嵌 PostgreSQL 数据（配置、记忆、调度器）；使用 `STELLA_DATABASE_URL` 时不存在 |
| `~/.stella/pg-runtime/`                       | 下载的内嵌 PostgreSQL runtime；可用 `stellad postgres download` 重建            |
| `~/.stella/bundles/{revision}/`               | 与发行版完全一致的 builtin Skill bundle；从匹配二进制派生                       |
| `~/.stella/agents/{agent-id}/.agents/skills/` | 派生的 `system_agent` Skill 执行缓存                                            |
| `~/.stella/agents/{agent-id}/SOUL.md`         | 可选的每个 agent 的灵魂/身份覆盖                                                |
| `~/.stella/cache/sandbox-tmp/`                | Docker 沙箱临时目录；属于临时数据，启动时删除遗留目录                           |

必须保留 PostgreSQL、包含 Project Skill 的持久 Agent/项目工作树，以及未镜像资产树。PostgreSQL 包含配置、消息历史、摘要、调度器任务和可变的 `system`、`system_agent`、`user`、`user_agent` Skill。使用内嵌集群时，停止服务后备份 `~/.stella/postgres/`；`~/.stella/pg-runtime/`、`~/.stella/bundles/{revision}/` 和 Skill 执行缓存都是派生数据，可重新生成。使用外部服务器时，对 `STELLA_DATABASE_URL` 所指数据库执行 `pg_dump`。

关于哪些目录属于持久数据、派生缓存或临时数据——以及各自在 Kubernetes 或临时磁盘上所需的卷与备份处理方式——完整说明参见[存储与持久化](/docs/start-here/storage)。

## 环境变量

配置通过Web UI管理（默认 `http://localhost:25678`；使用 `--port` 自定义端口）。还支持使用 `HOST` 和 `PORT` 绑定服务，其余仅支持少量环境变量：

| 变量                                       | 必需                      | 描述                                                                                                                   |
| ------------------------------------------ | ------------------------- | ---------------------------------------------------------------------------------------------------------------------- |
| `STELLA_HOME`                              | 否                        | Stella 主目录（默认 `~/.stella`）                                                                                      |
| `STELLA_DATABASE_URL`                      | Docker 中必需；其他环境否 | 外部 PostgreSQL 连接 URL；Docker 之外不设置时使用 `STELLA_HOME` 下的内嵌集群                                           |
| `STELLA_BASE_URL`                          | 否¶                       | OAuth 回调与频道外链使用的公网 canonical URL；未设置时由绑定地址推导（loopback）                                       |
| `STELLA_REQUIRE_EXTERNAL_DB`               | 否                        | `STELLA_DATABASE_URL` 未设置时快速失败而非启动内嵌 PostgreSQL；Docker 镜像默认设为 `1`，设 `0` 可在持久卷上运行内嵌 PG |
| `STELLA_STORAGE_MODE`                      | 否                        | `local`（默认）或 `shared-posix`；共享模式要求已审查认证记录与独立 freshness witness                                   |
| `STELLA_SHARED_POSIX_IDENTITY`             | 仅共享 POSIX              | 认证记录中的预期非敏感 namespace identity                                                                              |
| `STELLA_SHARED_POSIX_QUALIFICATION_SHA256` | 仅共享 POSIX              | 已安装且通过的准确认证记录 SHA-256                                                                                     |
| `STELLA_SHARED_POSIX_WITNESS_ID`           | 仅共享 POSIX              | 独立托管 freshness witness 的预期 identity                                                                             |
| `STELLA_STORAGE_CHECK_INTERVAL`            | 否                        | 共享 mount validation 间隔（Go duration，默认 `2s`）                                                                   |
| `STELLA_STORAGE_FRESHNESS_TIMEOUT`         | 否                        | 成功检查与 witness advance 的最大间隔（Go duration，默认 `15s`）                                                       |
| `STELLA_STORAGE_STARTUP_TIMEOUT`           | 否                        | 启动时等待跨客户端 witness advance 的预算（Go duration，默认 `20s`）                                                   |
| `STELLA_HTTP_SHUTDOWN_TIMEOUT`             | 否                        | 优雅停机时排空进行中 HTTP 请求的预算（Go duration，默认 `60s`，`> 0`）                                                 |
| `STELLA_RIVER_SOFT_STOP_TIMEOUT`           | 否                        | 优雅停机时排空进行中后台任务的预算（Go duration，默认 `120s`，`> 0`）                                                  |
| `STELLA_BLOB_S3_ENDPOINT`                  | 否§                       | immutable BlobStore 数据使用的 S3 兼容 endpoint                                                                        |
| `STELLA_BLOB_S3_BUCKET`                    | 否§                       | immutable BlobStore 数据使用的 bucket                                                                                  |
| `STELLA_BLOB_S3_ACCESS_KEY`                | 否§                       | immutable BlobStore 数据使用的 access key                                                                              |
| `STELLA_BLOB_S3_SECRET_KEY`                | 否§                       | immutable BlobStore 数据使用的 secret key                                                                              |
| `STELLA_BLOB_S3_REGION`                    | 否                        | 可选 S3 region                                                                                                         |
| `STELLA_BLOB_S3_USE_SSL`                   | 否                        | S3 兼容存储是否使用 HTTPS；默认 `true`                                                                                 |
| `STELLA_VAULT_KEY`                         | 是†                       | 密钥库使用的 age 私钥 —— 密钥管理、OAuth 和 Bearer Token 所必需                                                        |
| `STELLA_DOCKER_SANDBOX_MODE`               | 否‡                       | 仅 `docker` 沙箱后端需要：`host`、`bind` 或 `volume`                                                                   |
| `STELLA_HOME_HOST`                         | 否‡                       | `STELLA_HOME` 的宿主机侧路径；仅 `STELLA_DOCKER_SANDBOX_MODE=bind` 时需要                                              |
| `STELLA_HOME_VOLUME`                       | 否‡                       | `STELLA_HOME` 的 Docker named volume 名称；仅 `STELLA_DOCKER_SANDBOX_MODE=volume` 时需要                               |

† 未设置 `STELLA_VAULT_KEY` 时，密钥库接口返回 `503`，无法签发 OAuth Token，插件密钥也不会被注入。使用 `age-keygen` 生成密钥。

‡ 仅当 agent 使用 `docker` 沙箱后端时需要。stellad 在宿主机上运行用 `host`；stellad 在 Docker 内且使用 host bind mount 用 `bind`；stellad 在 Docker 内且使用 named volume 用 `volume`。

§ 四个必需的 S3 变量必须同时设置，或全部不设置。部分设置会导致启动失败；可变资产不需要这些变量。

¶ 受管部署所必需，以及在使用 OAuth 登录或频道外链时必需。参见[受管部署](#受管部署)。

共享 POSIX 变量与完整认证流程参见[认证共享 POSIX 存储](/docs/start-here/shared-posix)。这些变量不会开放多副本。

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
