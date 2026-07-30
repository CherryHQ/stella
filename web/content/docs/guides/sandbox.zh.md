---
title: 沙箱
---

Stella 在沙箱内运行 agent 代码。你可以为每个 agent 选择沙箱后端——不同后端提供不同的隔离级别、平台支持和权衡取舍。

## 选择后端

| 场景                                | 推荐     | 原因                                 |
| ----------------------------------- | -------- | ------------------------------------ |
| 生产环境 / 多用户                   | `docker` | 完整的容器级进程、文件系统和网络隔离 |
| 无 Docker 的 Linux                  | `local`  | 通过 bubblewrap 实现操作系统级隔离   |
| 无 Docker 的 macOS                  | `none`   | `local` 在 macOS 上不提供额外隔离    |
| Windows                             | `docker` | `local` 不支持 Windows               |
| 可信的单用户本地开发                | `none`   | 零依赖，无隔离                       |
| 自定义工具链（特定 Python/Node/Go） | `docker` | 独立于宿主机的干净 Linux 用户空间    |

在 Web UI 的 **Plugins** 页面切换活动沙箱后端。同一时间只有一个后端处于活动状态。

## Docker 后端

Docker 提供完整的容器级进程、文件系统和网络隔离。Docker 守护进程必须正在运行且可访问。支持所有平台（Linux、macOS、Windows）。

### 何时使用

- 需要 agent 与宿主机之间的强隔离。
- 需要带有特定工具链的可复现 Linux 环境。
- 在 Windows 上运行（唯一的沙箱选项）。
- 需要副作用隔离——agent 脚本无法修改挂载工作区之外的宿主机文件系统。

### 权衡取舍

- **启动延迟**：容器热启动约 200ms；首次拉取镜像约 1–3s。
- **绑定挂载性能**：在 macOS/Windows 的 Docker Desktop 上，绑定挂载文件系统操作比原生磁盘慢 5–20 倍。这些平台上有大量读写操作的工作流应避免使用。
- **无写时复制隔离**：与本地后端（在 Linux 上使用 overlayfs）不同，Docker 后端不提供基于 overlay 的 COW。失控脚本可能修改或损坏已挂载的工作区。

### 运行模式

当 stellad 本身运行在 Docker 容器内且 agent 使用 `docker` 沙箱后端时，你需要告诉 Stella Docker daemon 如何访问 `STELLA_HOME`。将 `STELLA_DOCKER_SANDBOX_MODE` 设置为以下之一：

| 模式     | 何时使用                                                | 需要的环境变量                                    |
| -------- | ------------------------------------------------------- | ------------------------------------------------- |
| `host`   | stellad 运行在宿主机上（不在容器内）                    | 不需要 `STELLA_HOME_HOST` 和 `STELLA_HOME_VOLUME` |
| `bind`   | stellad 运行在 Docker 内；`STELLA_HOME` 是 bind mount   | `STELLA_HOME_HOST` = 宿主机侧路径                 |
| `volume` | stellad 运行在 Docker 内；`STELLA_HOME` 是 named volume | `STELLA_HOME_VOLUME` = volume 名称                |

每种模式都会拒绝属于其他模式的环境变量。例如，`bind` 模式下设置 `STELLA_HOME_VOLUME` 会报错。

Volume 模式需要 Docker Engine 25+ 以支持 volume subpath 挂载。

### Docker Compose 示例

**容器内使用 `local` 或 `none` 沙箱** — 最简单的部署：

```yaml
services:
  stella:
    image: ghcr.io/cherryhq/stella:latest
    restart: unless-stopped
    security_opt:
      - seccomp=unconfined
    volumes:
      - ./stella-data:/home/stella/.stella
```

agent 使用 `local` 沙箱时保留 `seccomp=unconfined`（bubblewrap 需要它）；使用 `none` 时可以移除。

**容器内使用 `docker` 沙箱和 host bind mount：**

```yaml
services:
  stella:
    image: ghcr.io/cherryhq/stella:latest
    restart: unless-stopped
    volumes:
      - ./stella-data:/home/stella/.stella
      - /var/run/docker.sock:/var/run/docker.sock
    environment:
      - STELLA_DOCKER_SANDBOX_MODE=bind
      - STELLA_HOME_HOST=${PWD}/stella-data
```

**容器内使用 `docker` 沙箱和 named volume：**

```yaml
services:
  stella:
    image: ghcr.io/cherryhq/stella:latest
    restart: unless-stopped
    volumes:
      - stella-data:/home/stella/.stella
      - /var/run/docker.sock:/var/run/docker.sock
    environment:
      - STELLA_DOCKER_SANDBOX_MODE=volume
      - STELLA_HOME_VOLUME=stella-data

volumes:
  stella-data:
```

### 环境变量

| 变量                         | 描述                                                                |
| ---------------------------- | ------------------------------------------------------------------- |
| `STELLA_DOCKER_SANDBOX_MODE` | `docker` 沙箱后端必须设置：`host`、`bind` 或 `volume`               |
| `STELLA_HOME_HOST`           | `STELLA_HOME` 的宿主机侧路径；仅 `bind` 模式需要                    |
| `STELLA_HOME_VOLUME`         | `STELLA_HOME` 对应的 Docker named volume 名称；仅 `volume` 模式需要 |

agent 使用 `local` 或 `none` 时，这些变量都不需要。

## 本地后端

本地后端直接在宿主机 OS 上运行命令。适用于 Docker 不可用或不想使用的环境。

**此后端不提供容器级隔离。** 它使用操作系统级加固替代：

| 平台    | 隔离方式                                                                                           |
| ------- | -------------------------------------------------------------------------------------------------- |
| Linux   | `bwrap`（bubblewrap）— 必需。最小 Linux 根环境，`/workspace` 读写，隔离的 `/tmp`，网络命名空间控制 |
| macOS   | 无额外沙箱。命令直接在宿主机上运行                                                                 |
| Windows | 不支持，请使用 Docker 后端                                                                         |

### 安装 bubblewrap（Linux）

```bash
# Debian / Ubuntu
apt install bubblewrap

# Fedora / RHEL
dnf install bubblewrap

# Arch
pacman -S bubblewrap
```

bubblewrap 必须实际可用，仅安装不够。在未启用 `--privileged` 或 `seccomp=unconfined` 的 Docker 容器内，内核 seccomp 配置会阻止命名空间创建——此类环境请改用 Docker 后端。

### 路径呈现

隔离型后端（Linux `local`，经 bubblewrap；以及 `docker`）呈现固定的**双根**布局，与真实宿主机路径无关：

| 沙箱路径      | 实际来源                   | 访问权限 | 存放内容                                    |
| ------------- | -------------------------- | -------- | ------------------------------------------- |
| `/workspace`  | 该 agent 的 per-agent 目录 | 读写     | `$HOME` 与项目工作树——仅属于这一个 agent    |
| `/user`       | 该用户的共享数据根目录     | 读写     | 该用户所有 agent 共享的数据（见下）         |
| `/opt/stella` | 系统安装树                 | 只读     | 系统二进制、共享 mise 工具链、系统级 skills |

系统树中只有 `bin`、`.mise-tools`、`.agents/skills` 三个子树被挂到 `/opt/stella`——`STELLA_HOME` 下的兄弟目录 `users/`、`agents/` 不会在此暴露。

Linux `local` 后端会把按 principal 划分的临时目录挂为沙箱内 `/tmp`；Docker `bind`/`host` 模式在该目录对 Docker daemon 可见时同样如此，而 Docker `volume` 模式不挂载宿主机临时目录。临时文件因此按用户隔离。在 macOS（`local`）和 `none` 后端下不做重映射——agent 看到的是真实宿主机路径，因此这两个根呈现在其真实位置，而非 `/workspace` 与 `/user`。

### 主目录与共享数据

在隔离型沙箱内，`$HOME` 即 `/workspace`——agent **自己的 per-agent** 目录与项目工作树。直接写入 `$HOME`（例如 `~/.tool`）的工具仍会把状态留在该 agent 内。

持久化 XDG 目录属于用户级数据，并由该用户的所有 agent 共享：`XDG_CONFIG_HOME=/user/.config`、`XDG_DATA_HOME=/user/.local/share`、`XDG_STATE_HOME=/user/.local/state`、`XDG_CACHE_HOME=/user/.cache`。因此，遵循 XDG 的工具可以复用配置与登录状态，而 agent 的工作内容仍留在 `/workspace`。`XDG_RUNTIME_DIR` 不会持久化。没有用户数据根目录的会话则把这四个目录都放回自身 workspace。

共享用户数据根目录也通过 `$STELLA_USER_DIR` 暴露，用于存放用户级 skills 与上传资产（`/user/assets`）。请用 `$STELLA_USER_DIR/...` 引用该根，而非硬编码 `/user`——这样在 `none`/macOS 后端（解析为真实宿主机路径）下同样有效。

Docker 后端把 mise 工具链烤在绝对 `/opt/stella` 下——与 Linux `local` 后端把 `STELLA_HOME` 重映射到的路径一致——因此无论哪个隔离后端运行，agent 看到的 mise 路径都相同，切换后端不影响工具解析。`MISE_DATA_DIR` 等被钉死在该树上，所以把 `$HOME` 翻到 `/workspace` 不会隐藏内置工具。镜像通过与宿主相同的 `resources/tools.yaml` reconcile 安装内置工具，per-user 可写 mise 树挂载在 `/opt/stella/users/{id}/.mise-tools`，agent 可在共享基础上安装自己的工具。

## None 后端

`none` 后端以当前用户权限直接在宿主机上运行 agent，不提供任何隔离——无文件系统限制、无网络限制、无进程组终止，也无资源限制。

**仅在单用户本地部署中对完全受信任的 agent 使用。** 不适用于不受信任的 agent 或多用户环境。

- 无外部依赖——适用于所有平台。
- 网络策略始终为 `allow_all`；每个 agent 配置的网络模式将被忽略。
- 不会因缺少工具而导致会话创建失败。

## 网络策略

每个 agent 独立控制其沙箱是否允许出站网络访问。在 Web UI 的 agent 沙箱设置中配置。

| 模式        | 描述                         |
| ----------- | ---------------------------- |
| `disabled`  | 禁止所有出站网络访问（默认） |
| `allow_all` | 不受限制的出站访问           |

Docker 和 Linux 本地后端会在会话创建时验证网络模式，如果后端无法强制执行则失败。macOS 本地后端当前会忽略网络策略。

## 故障排查

**bubblewrap 在 Docker 容器内失败：**
内核 seccomp 配置阻止命名空间创建。在 Docker run 命令或 compose 文件中添加 `--security-opt seccomp=unconfined`。或者切换到 `docker` 沙箱后端。

**Docker 守护进程不可达：**
会话创建失败，runner 不会启动。确保 Docker 守护进程正在运行且 socket 可访问。在 Docker 内运行 stellad 时，挂载 `/var/run/docker.sock`。

**Volume 模式："workspace is not inside STELLA_HOME"：**
在 volume 模式下，所有沙箱工作区必须是 `STELLA_HOME` 的子目录。此错误意味着工作区路径解析到了 volume 边界之外。检查 `STELLA_HOME` 和 `STELLA_HOME_VOLUME` 配置是否正确。

**Xberg 无法加载 `libheif`：**
Stella 的 Docker 镜像已包含兼容版本。本机 Linux 部署需要 libheif 1.21 或更高版本；Debian 13 的软件包版本过旧。macOS 可运行 `brew install libheif` 安装。如果无法提供兼容的本机动态库，请使用 Docker 沙箱后端。

**macOS/Windows 上绑定挂载性能慢：**
Docker Desktop 对绑定挂载使用虚拟化文件系统层。对于高 I/O 工作负载，考虑使用 named volume（`volume` 模式）或在宿主机上原生运行 stellad（`host` 模式）。
