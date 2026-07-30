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

### Agent 文件系统契约

在 Agent 指令中使用下列环境变量。它们是 Agent 工作时的文件系统 API；字面量沙箱路径仅用于说明后端渲染、兼容性或命令输出细节。`read`、`write` 和 `edit` 工具能识别全部四个根路径；`share` 接受 `$HOME`、`$STELLA_ASSETS_DIR` 和兼容变量 `$STELLA_USER_DIR`，但不接受 `$TMPDIR`。不要在 Agent 指令中硬编码 `/workspace`、`/user` 或 `/tmp`。

| 根路径               | 用途                                                     | 规则                                                           |
| -------------------- | -------------------------------------------------------- | -------------------------------------------------------------- |
| `$HOME`              | 持久、私有的每 Agent 工作区，用于项目和默认工作          | 相对路径在当前项目/工作目录中解析。                            |
| `$STELLA_ASSETS_DIR` | 可用时，存放同一用户或群组共享的持久上传文件和最终交付物 | 这是托管用户/群组根目录下通常允许直接写入的位置。              |
| `$TMPDIR`            | 可丢弃的临时工作区                                       | 保留与复用行为取决于后端。不要放置最终输出，也不要依赖其存活。 |
| `$STELLA_USER_DIR`   | 用于兼容性和高级用途的托管用户/群组根目录                | 不要直接写入 `$STELLA_ASSETS_DIR` 之外的位置。                 |

### 托管用户与群组根目录

`XDG_CONFIG_HOME`、`XDG_DATA_HOME`、`XDG_STATE_HOME` 和 `XDG_CACHE_HOME` 由同一用户或群组的 Agent 共享，并由命令行工具托管。它们不是通用的 Agent 存储位置。没有用户/群组根目录时，这四个目录都会回退到 `$HOME` 下。`XDG_RUNTIME_DIR` 未设置。

mise、Lark 和系统目录由其工具托管，不是通用存储位置。

### 后端路径渲染

下列字面量路径描述进程视图，而不是 Agent 文件系统 API：

| 后端或条件                | 进程视图                                                                                                                     |
| ------------------------- | ---------------------------------------------------------------------------------------------------------------------------- |
| Linux `local` 和 `docker` | 通常 `$HOME=/workspace`、`$STELLA_ASSETS_DIR=/user/assets`、`$TMPDIR=/tmp`；`$STELLA_USER_DIR=/user` 是托管用户/群组根目录。 |
| macOS `local` 和 `none`   | 进程看到实际宿主机路径，而不是重映射后的沙箱路径。                                                                           |
| Docker 缺少 `/user`       | `$STELLA_ASSETS_DIR` 和 `$STELLA_USER_DIR` 不存在，XDG 目录会回退到 `$HOME`/工作区下。                                       |

Linux `local` 后端可以将按用户或群组划分的临时目录挂载到 `/tmp`。Docker `bind` 和 `host` 模式在该目录对 Docker daemon 可见时也可以这样做，而 `volume` 模式不会挂载该宿主机临时目录。因此，临时目录的生命周期和复用因后端而异；不要承诺其在各后端下具有统一的按用户或按会话持久性。

隔离型后端还会将系统安装树以只读方式渲染到 `/opt/stella`。其中只挂载 `bin`、`.mise-tools` 和 `.agents/skills` 子树；`STELLA_HOME` 下同级的 `users/` 和 `agents/` 树不会暴露。Docker 后端会将 mise 工具链置于该路径，Linux `local` 则在该路径渲染对应的系统树，因此工具解析在各隔离型后端之间保持一致。`MISE_DATA_DIR` 及相关变量固定指向这个由工具托管的目录树。

### 升级现有工作区

升级不会移动、合并或删除既有的 Agent 本地 XDG 文件。升级后，遵循 XDG 的命令行工具可能需要为该用户或群组重新设置或登录一次。只有在理解该工具数据的情况下才进行特定工具的手动迁移；缓存可以自行重新生成。

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
