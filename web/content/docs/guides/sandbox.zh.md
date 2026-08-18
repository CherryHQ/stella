---
title: 沙箱
---

Stella 在沙箱内运行 agent 代码。沙箱后端由运维在部署时统一选定——不同后端提供不同的隔离级别、平台支持和权衡取舍。

## 选择后端

| 场景                                | 推荐     | 原因                                 |
| ----------------------------------- | -------- | ------------------------------------ |
| 生产环境 / 多用户                   | `docker` | 完整的容器级进程、文件系统和网络隔离 |
| 无 Docker 的 Linux                  | `local`  | 通过 bubblewrap 实现操作系统级隔离   |
| 无 Docker 的 macOS                  | `none`   | `local` 在 macOS 上不提供额外隔离    |
| 原生 Windows 宿主机                 | —        | 不支持原生 `stellad` 服务端          |
| 可信的单用户本地开发                | `none`   | 零依赖，无隔离                       |
| 自定义工具链（特定 Python/Node/Go） | `docker` | 独立于宿主机的干净 Linux 用户空间    |

部署时通过 `STELLA_SANDBOX_BACKEND` 环境变量指定后端，然后重启 `stellad`：

```bash
STELLA_SANDBOX_BACKEND=docker   # docker | local | none
```

默认值是 `local`。未设置或取值无法识别时同样回落到 `local`，因此拼错变量不会让 agent 失去隔离。没有 Web UI 开关，也没有 per-agent 覆盖——沙箱边界是运维决策，不是运行时决策。

## Docker 后端

Docker 提供完整的容器级进程、文件系统和网络隔离。在受支持的 Linux 或 macOS 服务端宿主机上，Docker 守护进程必须正在运行且可访问。

### 何时使用

- 需要 agent 与宿主机之间的强隔离。
- 需要带有特定工具链的可复现 Linux 环境。
- 需要副作用隔离——agent 脚本无法修改挂载工作区之外的宿主机文件系统。

### 权衡取舍

- **启动延迟**：容器热启动约 200ms；首次拉取镜像约 1–3s。
- **绑定挂载性能**：在 macOS 的 Docker Desktop 上，绑定挂载文件系统操作比原生磁盘慢 5–20 倍。大量读写操作的工作流应避免使用。
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

### OCI Runtime

默认情况下，沙箱容器使用 Docker daemon 的默认开放容器倡议（OCI）runtime，通常是 `runc`。将 `STELLA_DOCKER_RUNTIME` 设为该 daemon 已注册的 runtime，可以选择更强或专用的执行边界：

```bash
STELLA_DOCKER_RUNTIME=runsc
```

`runsc` 是 gVisor runtime。启用前先安装并注册到 Docker，再确认它出现在 `docker info` 中。Stella 预检会拒绝不可用的已配置 runtime，不会回退到 daemon 默认值。所选 runtime 同时用于 agent 会话和 Docker 工具缓存辅助容器。

替代 OCI runtime 可以减少宿主内核暴露面，但不会限制网络出口，也不会保护可写挂载。沙箱网络策略和挂载权限仍需独立收紧。

Stella 还会检测 Docker daemon 是否为 rootless。rootful daemon 使用 `stellad` 的 UID/GID 运行沙箱进程；rootless daemon 使用容器 UID/GID `0:0`，它在宿主机上映射为非特权 daemon 用户，并保持该用户的 bind mount 可写。两种模式都会继续丢弃 capabilities 并启用 `no-new-privileges`。如果 rootless daemon 没有 cgroup driver，Stella 会在预检时拒绝启动，因为此时无法执行 CPU、内存和 PID 限制；生产环境不要为 gVisor 配置 `--ignore-cgroups`。

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
      - STELLA_DOCKER_RUNTIME=runsc # 可选；必须已注册到 Docker
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
      - STELLA_DOCKER_RUNTIME=runsc # 可选；必须已注册到 Docker
      - STELLA_HOME_VOLUME=stella-data

volumes:
  stella-data:
```

### 环境变量

| 变量                         | 描述                                                                |
| ---------------------------- | ------------------------------------------------------------------- |
| `STELLA_SANDBOX_BACKEND`     | 部署使用的沙箱后端：`docker`、`local`（默认）或 `none`              |
| `STELLA_DOCKER_SANDBOX_MODE` | `docker` 沙箱后端必须设置：`host`、`bind` 或 `volume`               |
| `STELLA_DOCKER_RUNTIME`      | Docker 沙箱容器使用的可选已注册 OCI runtime，例如 `runsc`           |
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
| Windows | 不支持原生 `stellad` 服务端                                                                        |

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

在 Agent 指令中使用下列环境变量。它们是 Agent 工作时的文件系统 API；字面量沙箱路径仅用于说明后端渲染、兼容性或命令输出细节。`read`、`write` 和 `edit` 工具能识别全部三个根路径；`share` 接受 `$HOME` 和 `$STELLA_ASSETS_DIR`，但不接受 `$TMPDIR`。不要在 Agent 指令中硬编码 `/workspace`、`/user` 或 `/tmp`。

| 根路径               | 用途                                                     | 规则                                                 |
| -------------------- | -------------------------------------------------------- | ---------------------------------------------------- |
| `$HOME`              | 持久、私有的每 Agent 工作区，用于项目和默认工作          | 相对路径在当前项目/工作目录中解析。                  |
| `$STELLA_ASSETS_DIR` | 可用时，存放同一用户或群组共享的持久上传文件和最终交付物 | 这是供 Agent 直接写入的托管共享位置。                |
| `$TMPDIR`            | 每个会话私有的可丢弃临时工作区                           | 不要放置最终输出，也不要依赖它在会话结束后继续存在。 |

Web Workspace API 使用类型化 scope 与规范相对路径定位文件。项目 `base_dir` 同样相对于 Agent workspace（`.` 表示其根）。这些 API 在授权后直接打开持久 POSIX 根，不会启动或唤醒 Session compute。返回的 `root` 是逻辑 `/workspace` 或 `/user` 根，绝不包含宿主路径。活跃 Agent 工具仍通过既有 Session mount 与策略边界解析路径。

### 托管用户与群组根目录

`XDG_CONFIG_HOME`、`XDG_DATA_HOME`、`XDG_STATE_HOME` 和 `XDG_CACHE_HOME` 由同一用户或群组的 Agent 共享，并由命令行工具托管。它们不是通用的 Agent 存储位置。没有用户/群组根目录时，这四个目录都会回退到 `$HOME` 下。`XDG_RUNTIME_DIR` 未设置。

这些 XDG 目录按 principal 共享，因此落盘的 CLI 登录或配置（其中可能包含凭据）会对该 principal 的所有 Agent 可见。需要按 Agent 隔离认证时，请将凭据存入 [Agent 专属的 Vault scope](/docs/guides/secrets-and-keys)，并使用 CLI 基于环境变量的认证方式，而不是持久化登录。

mise、Lark 和系统目录由其工具托管，不是通用存储位置。mise 会先加载 Stella 发行版提供的只读系统工具层，再加载 principal 共享的全局配置，最后加载当前 workspace 配置。使用 `mise use --global --pin <tool>@<version>` 安装个人默认工具；该命令只写共享全局配置，不会修改 Stella 的系统工具。属于特定项目的版本则应在项目内运行 `mise use --pin <tool>@<version>`。嵌套 login shell 重置 `PATH` 后，Stella 也会恢复这些托管工具路径，因此通过 `bash -lc` 启动的命令仍能解析同一组 principal 和系统工具。

### 后端路径渲染

下列字面量路径描述进程视图，而不是 Agent 文件系统 API：

| 后端或条件                | 进程视图                                                                     |
| ------------------------- | ---------------------------------------------------------------------------- |
| Linux `local` 和 `docker` | 通常 `$HOME=/workspace`、`$STELLA_ASSETS_DIR=/user/assets`、`$TMPDIR=/tmp`。 |
| macOS `local` 和 `none`   | 进程看到实际宿主机路径，而不是重映射后的沙箱路径。                           |
| Docker 缺少 `/user`       | `$STELLA_ASSETS_DIR` 不存在，XDG 目录会回退到 `$HOME`/工作区下。             |

每个后端都会为每个沙箱会话创建私有临时目录，并在会话关闭时删除。Docker 的 backing 目录位于 `$STELLA_HOME/cache/sandbox-tmp/` 下并挂载到 `/tmp`，因此 shell 命令和文件工具访问的是同一份内容；启动清理会删除遗留的 Docker 临时目录。这是临时工作区，不承诺持久性。

隔离型后端还会将系统安装树以只读方式渲染到 `/opt/stella`。其中保留由工具托管的 `bin` 和 `.mise-tools` 树，builtin 位于 `/opt/stella/skills/builtin`；`STELLA_HOME` 下同级的 `users/` 和 `agents/` 树不会暴露。选中的 managed Skill 会单独复制到 `$TMPDIR` 下按 digest 固定的 Session 私有目录；其完整 authority root 与 revision history 绝不会挂载进沙箱。Docker 后端会将 mise 工具链置于 `/opt/stella`，Linux `local` 则在该路径渲染对应的系统树，因此工具解析在各隔离型后端之间保持一致。mise 的系统配置保留在这棵只读树中；principal 全局配置可写并位于 `XDG_CONFIG_HOME` 下，而 installs、cache 和 state 继续放在 Stella 单独托管的 per-principal 树中，以保证相对工具链接在不同后端下得到相同解析结果。

### builtin Skill bundle

原生 `local` 和 `none` 安装使用 `$STELLA_HOME/bundles/<revision>` 中与发行版完全一致的 bundle。只读挂载 `/opt/stella/skills/builtin` 只是隔离执行视图，绝不是第二个权威。bundle 中辅助可执行文件的模式会被保留。

Docker 沙箱镜像会烤入并标记同一 revision，且不会回退到宿主机 builtin。Docker provider preflight 会拒绝 revision 与运行中的 Stella 二进制不匹配的组合，因此 runner session 不会启动。命令语法请运行 `stellad system-bundle --help`。开发者重建本地沙箱镜像时运行 `mise run sandbox:docker:build`；自定义沙箱镜像必须从匹配的 Stella revision 重建。

升级前，请使用旧的可工作二进制，将遗留 `$STELLA_HOME/.agents/skills` 下的每个自定义 Skill 根导入为全局（`system`）Skill：旧版入口为 **设置 → 技能**，新版入口为 **管理控制台 → 部署资源 → 全局技能**。其他残留路径应先备份、验证后删除。启动会列出每个阻塞路径并停止，不会删除或修改任何内容。当前发行 manifest 所拥有的路径即使内容或模式陈旧也只是惰性数据；其他每个 Skill 根或残留路径都会阻塞启动。

### 升级现有工作区

升级不会移动、合并或删除既有的 Agent 本地 XDG 文件。升级后新建的 CLI 状态按 principal 共享，不再归单个 Agent 私有，因此持久化 CLI 登录会对同一用户或群组的其他 Agent 可见。遵循 XDG 的命令行工具可能需要为该用户或群组重新设置或登录一次。只有在理解该工具数据的情况下才进行特定工具的手动迁移；缓存可以自行重新生成。

`STELLA_USER_DIR` 会被直接移除，不保留兼容路径。原先使用它的 Agent 指令和 share 表达式，应按用途改为 `$STELLA_ASSETS_DIR`（共享交付物）或 `$HOME`（Agent 私有工作）。

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

**升级后 Xberg 不可用：**
当前 Linux 和 macOS 版本已内置 Xberg 及其原生动态库。请使用升级后的 `stellad` 二进制重启 Stella，让它把匹配的运行时安装到 `STELLA_HOME`；不要另行安装 `libheif`。

**macOS/Windows 上绑定挂载性能慢：**
Docker Desktop 对绑定挂载使用虚拟化文件系统层。对于高 I/O 工作负载，考虑使用 named volume（`volume` 模式）或在宿主机上原生运行 stellad（`host` 模式）。
