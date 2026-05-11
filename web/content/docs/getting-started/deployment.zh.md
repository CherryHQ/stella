---
title: 部署
---

两种部署方式：**二进制文件**（直接安装）和 **Docker**。

## 二进制文件

### 从发布版本安装

从 [GitHub Releases](https://github.com/CherryHQ/stella/releases) 下载预构建的二进制文件。支持 linux、macOS 和 Windows 平台的 amd64/arm64 架构。

```bash
# 示例：Linux amd64
curl -LO https://github.com/CherryHQ/stella/releases/latest/download/stella_linux_amd64.tar.gz
tar xzf stella_linux_amd64.tar.gz
chmod +x stella
sudo mv stella /usr/local/bin/
```

### 从源码构建

```bash
go install github.com/CherryHQ/stella@latest
# 或
git clone https://github.com/CherryHQ/stella.git
cd stella && go build -o stella .
```

### 运行

运行 onboard 命令打开管理面板并配置 stella（提供商、频道、agents 等）：

```bash
stella --open
```

这会启动一个本地 Web UI，您可以在其中设置 API 密钥、频道和 agent 配置。所有配置都存储在 `~/.stella/stella.db` 中 —— 无需手动配置文件。

启动网关守护进程：

```bash
stella
```

要在网关运行时同时提供管理面板服务（用于运行时配置更改）：

```bash
stella --port 8080
stella --host 0.0.0.0 --port 8080
```

### 版本和自动升级

```bash
stella version
stella upgrade
stella upgrade --install-dir "$HOME/.local/bin"
```

`stella upgrade` 从 GitHub 获取最新稳定版本，下载与当前操作系统/架构匹配的安装包，并将二进制文件安装到 `$HOME/.local/bin`（默认位置）。

### Systemd 服务（Linux）

使用内置的 service 命令 —— 它会写入 unit 文件、执行 `daemon-reload` 并一步完成服务启用：

```bash
# 用户模式（无需 root，登录时启动）
stella service install
stella service status
stella service logs --follow
stella service uninstall

# 系统模式（需要 root，开机启动）
sudo stella service install --system
stella service status
sudo stella service uninstall --system
```

运行 `stella service install` 前需先安装 `bubblewrap`。通过 Homebrew 或包管理器安装时会自动拉取；直接使用二进制文件时请手动安装：`apt install bubblewrap` / `dnf install bubblewrap`。

手动或系统管理员使用的参考 unit 文件位于 [`scripts/stella.service`](https://github.com/CherryHQ/stella/blob/main/scripts/stella.service)。

### boxsh 沙盒前置条件（Linux）

在 Linux 上，Stella 默认使用托管的 `boxsh` 沙盒来运行本地工作区工具（`bash`、`read`、`write`、`edit`）。`boxsh` 需要宿主机支持用户命名空间和从属 ID 映射。

安装用户命名空间辅助工具：

```bash
# Debian / Ubuntu
sudo apt update
sudo apt install uidmap

# 验证辅助工具是否存在
which newuidmap
which newgidmap
ls -l /usr/bin/newuidmap /usr/bin/newgidmap
```

确保服务用户具有从属 UID/GID 范围：

```bash
grep '^stella:' /etc/subuid
grep '^stella:' /etc/subgid
```

预期格式：

```text
stella:100000:65536
```

如果缺少这些条目，请添加它们：

```bash
sudo usermod --add-subuids 100000-165535 stella
sudo usermod --add-subgids 100000-165535 stella
```

验证内核是否允许非特权用户命名空间：

```bash
sysctl kernel.unprivileged_userns_clone
sysctl user.max_user_namespaces
```

典型的工作值：

```text
kernel.unprivileged_userns_clone = 1
user.max_user_namespaces = 15000
```

某些 Ubuntu 主机即使启用了上述内核设置，仍可能通过 AppArmor 阻止非特权用户命名空间。请检查：

```bash
sysctl kernel.apparmor_restrict_unprivileged_userns
```

如果 `boxsh` 失败并显示 `sandbox_apply failed: write uid_map: Operation not permitted`，请临时禁用该限制并重新测试：

```bash
sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0
```

要使 Linux 前置条件在重启后持久生效：

```bash
sudo tee /etc/sysctl.d/99-stella-boxsh.conf >/dev/null <<'EOF'
kernel.unprivileged_userns_clone=1
user.max_user_namespaces=15000
kernel.apparmor_restrict_unprivileged_userns=0
EOF

sudo sysctl --system
```

以服务用户身份直接对 `boxsh` 进行冒烟测试：

```bash
$STELLA_HOME/bin/boxsh --version
$STELLA_HOME/bin/boxsh --rpc --sandbox
```

如果第二个命令立即以 `uid_map` 错误退出，说明宿主机仍在阻止用户命名空间设置。如果您需要在修复宿主机之前让 Stella 工作，请将 agent 沙盒后端配置为 `docker` 作为临时回退（需要可访问的 docker 守护进程）。

### LaunchAgent（macOS）

使用内置的 service 命令 —— 它会写入 plist、加载 agent 并启动服务：

```bash
stella service install       # 安装 LaunchAgent 并启动
stella service status
stella service logs --follow
stella service stop
stella service start
stella service restart
stella service uninstall
```

日志写入 `~/Library/Logs/stella/stella.log`。agent 在登录时自动启动，崩溃后自动重启。

手动使用的参考 plist 文件位于 [`scripts/com.cherryai.stella.plist`](https://github.com/CherryHQ/stella/blob/main/scripts/com.cherryai.stella.plist)。

## Docker

镜像发布到 `ghcr.io/cherryhq/stella`，支持 `linux/amd64` 和 `linux/arm64` 平台。

### 标签

| 标签           | 描述         |
| -------------- | ------------ |
| `latest`       | 最新稳定版本 |
| `v1.2.3`       | 特定版本     |
| `sha-<commit>` | 特定提交     |

### 快速开始

首先，运行 onboard 来配置 stella：

```bash
docker run -it --rm \
  --security-opt seccomp=unconfined \
  -v ~/.stella:/home/nonroot/.stella \
  -p 8080:8080 \
  ghcr.io/cherryhq/stella:latest \
  stella --open
```

然后启动网关：

```bash
docker run -d \
  --name stella \
  --security-opt seccomp=unconfined \
  -v ~/.stella:/home/nonroot/.stella \
  -e ANTHROPIC_API_KEY=sk-... \
  ghcr.io/cherryhq/stella:latest
```

容器以 `nonroot` 用户运行。挂载 `~/.stella` 以持久化数据库、技能和缓存。您可以设置 `STELLA_HOME` 来更改容器内的数据目录。如果要在 Docker 中使用默认的 boxsh 沙箱，请在运行时加上 `--security-opt seccomp=unconfined`，这样 boxsh 才能调用 `unshare(2)`。没有这个选项时，带沙箱的核心工具会因为 Docker 运行时限制而失败。

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
      - ./stella-data:/home/nonroot/.stella
    environment:
      - ANTHROPIC_API_KEY=sk-...
      # - OPENAI_API_KEY=sk-...
```

```bash
docker compose up -d
```

要运行初始设置，使用 `docker compose exec stella stella --open` 或使用 `--port 8080` 启动网关并通过 Web UI 进行配置。

### 本地构建

```bash
# 单平台
docker build -t stella .

# 多平台
docker buildx build --platform linux/amd64,linux/arm64 -t stella .
```

## 使用 Docker 作为沙盒后端

将 stella 运行在 Docker 容器中（见上文）与使用 Docker 作为 agent 工具执行的沙箱后端是两件独立的事。两者可以结合使用（Docker-in-Docker 或挂载 socket），但各自独立也有价值。

### 何时优先选择 `docker` 沙箱后端

- **Windows**：`boxsh` 仅支持 Linux/macOS。`docker` 后端通过 Docker Desktop 为 Windows 用户提供真正的隔离边界。
- **自定义工具链**：需要与宿主机不同的特定 Python/Node/Go 版本，或需要干净的 Linux 用户空间。
- **副作用隔离**：需要可复现的文件系统状态，不希望 agent 脚本产生宿主机级别的副作用。

### 权衡取舍

- **启动延迟**：容器热启动约 200ms；首次拉取镜像约 1–3s。
- **绑定挂载性能**：在 macOS/Windows 的 Docker Desktop 上，绑定挂载文件系统操作比原生磁盘慢 5–20 倍。在这些平台上，有大量读写操作的工作流应避免使用 `docker` 后端。
- **无写时复制隔离**：与 `boxsh`（使用 overlayfs）不同，docker 后端不提供基于 overlay 的 COW。失控脚本可能修改或损坏已挂载的工作区。

有关 `sandbox.docker` 配置键和 JSON 示例，请参阅[配置指南](/docs/getting-started/configuration)。

## 卷和数据

所有数据都存储在 stella 主目录下（默认为 `~/.stella`，可通过 `STELLA_HOME` 配置）。

| 路径                                      | 用途                             |
| ----------------------------------------- | -------------------------------- |
| `~/.stella/stella.db`                     | 单一数据库（配置、记忆、调度器） |
| `~/.stella/workspaces/{agent-id}/skills/` | 每个 agent 安装的技能            |
| `~/.stella/workspaces/{agent-id}/SOUL.md` | 可选的每个 agent 的灵魂/身份覆盖 |
| `~/.stella/cache/`                        | 模型缓存（可重新生成，安全删除） |

`stella.db` 文件是唯一需要备份的关键数据。它包含所有配置、消息历史、摘要和调度器任务。

## 环境变量

配置通过管理面板管理（通过 `stella --open` 或 `--port`）。还支持使用 `HOST` 和 `PORT` 绑定管理面板，其余仅支持少量环境变量：

| 变量                | 必需 | 描述                              |
| ------------------- | ---- | --------------------------------- |
| `STELLA_HOME`       | 否   | Stella 主目录（默认 `~/.stella`） |
| `ANTHROPIC_API_KEY` | 是\* | Anthropic 提供商密钥              |
| `OPENAI_API_KEY`    | 是\* | OpenAI 提供商密钥                 |

\* 至少需要一个提供商密钥。API 密钥也可以通过管理面板配置。

## 健康检查

网关会记录日志到标准输出。验证它是否正在运行：

```bash
# 二进制文件
stella  # 日志显示在终端中

# Docker
docker logs stella
```
