---
title: 部署
---

两种部署方式：**二进制文件**（直接安装）和 **Docker**。

## 二进制文件

### 从发布版本安装

从 [GitHub Releases](https://github.com/vaayne/anna/releases) 下载预构建的二进制文件。支持 linux、macOS 和 Windows 平台的 amd64/arm64 架构。

```bash
# 示例：Linux amd64
curl -LO https://github.com/vaayne/anna/releases/latest/download/anna_linux_amd64.tar.gz
tar xzf anna_linux_amd64.tar.gz
chmod +x anna
sudo mv anna /usr/local/bin/
```

### 从源码构建

```bash
go install github.com/vaayne/anna@latest
# 或
git clone https://github.com/vaayne/anna.git
cd anna && go build -o anna .
```

### 运行

运行 onboard 命令打开管理面板并配置 anna（提供商、频道、agents 等）：

```bash
anna --open
```

这会启动一个本地 Web UI，您可以在其中设置 API 密钥、频道和 agent 配置。所有配置都存储在 `~/.anna/anna.db` 中 —— 无需手动配置文件。

启动网关守护进程：

```bash
anna
```

要在网关运行时同时提供管理面板服务（用于运行时配置更改）：

```bash
anna --port 8080
anna --host 0.0.0.0 --port 8080
```

### 版本和自动升级

```bash
anna version
anna upgrade
anna upgrade --install-dir "$HOME/.local/bin"
```

`anna upgrade` 从 GitHub 获取最新稳定版本，下载与当前操作系统/架构匹配的安装包，并将二进制文件安装到 `$HOME/.local/bin`（默认位置）。

### Systemd 服务（Linux）

项目提供了现成的 unit 文件：[`scripts/anna.service`](https://github.com/vaayne/anna/blob/main/scripts/anna.service)。

```bash
# 创建专用用户
sudo useradd --system --no-create-home --shell /bin/false anna
sudo mkdir -p /home/anna/.anna
sudo chown anna:anna /home/anna/.anna

# 安装 unit 文件，自动替换实际的 anna 二进制路径
sudo sed "s|ANNA_BIN|$(which anna)|g" scripts/anna.service \
  > /etc/systemd/system/anna.service
sudo systemctl daemon-reload
sudo systemctl enable --now anna
sudo journalctl -u anna -f   # 跟踪日志
```

所有配置（频道、agents、调度器任务）都存储在 `anna.db` 中。使用 `anna --open` 来访问管理面板管理配置。

### LaunchAgent（macOS）

项目提供了现成的 plist 文件：[`scripts/com.vaayne.anna.plist`](https://github.com/vaayne/anna/blob/main/scripts/com.vaayne.anna.plist)。

```bash
# 安装 —— 自动替换 $HOME 路径和 anna 二进制路径
sed "s|HOME_DIR|$HOME|g; s|ANNA_BIN|$(which anna)|g" scripts/com.vaayne.anna.plist \
  > ~/Library/LaunchAgents/com.vaayne.anna.plist
mkdir -p ~/Library/Logs/anna

launchctl load ~/Library/LaunchAgents/com.vaayne.anna.plist

# 管理
launchctl start com.vaayne.anna
launchctl stop  com.vaayne.anna

# 卸载
launchctl unload ~/Library/LaunchAgents/com.vaayne.anna.plist
rm ~/Library/LaunchAgents/com.vaayne.anna.plist
```

日志写入 `~/Library/Logs/anna/anna.log`。agent 在登录时自动启动，崩溃后自动重启。API 密钥和其他所有配置均通过 `anna --open` 或管理面板设置。

## Docker

镜像发布到 `ghcr.io/vaayne/anna`，支持 `linux/amd64` 和 `linux/arm64` 平台。

### 标签

| 标签           | 描述         |
| -------------- | ------------ |
| `latest`       | 最新稳定版本 |
| `v1.2.3`       | 特定版本     |
| `sha-<commit>` | 特定提交     |

### 快速开始

首先，运行 onboard 来配置 anna：

```bash
docker run -it --rm \
  --security-opt seccomp=unconfined \
  -v ~/.anna:/home/nonroot/.anna \
  -p 8080:8080 \
  ghcr.io/vaayne/anna:latest \
  anna --open
```

然后启动网关：

```bash
docker run -d \
  --name anna \
  --security-opt seccomp=unconfined \
  -v ~/.anna:/home/nonroot/.anna \
  -e ANTHROPIC_API_KEY=sk-... \
  ghcr.io/vaayne/anna:latest
```

容器以 `nonroot` 用户运行。挂载 `~/.anna` 以持久化数据库、技能和缓存。您可以设置 `ANNA_HOME` 来更改容器内的数据目录。如果要在 Docker 中使用默认的 boxsh 沙箱，请在运行时加上 `--security-opt seccomp=unconfined`，这样 boxsh 才能调用 `unshare(2)`。没有这个选项时，带沙箱的核心工具会因为 Docker 运行时限制而失败。

### Docker Compose

```yaml
# docker-compose.yml
services:
  anna:
    image: ghcr.io/vaayne/anna:latest
    restart: unless-stopped
    security_opt:
      - seccomp=unconfined
    volumes:
      - ./anna-data:/home/nonroot/.anna
    environment:
      - ANTHROPIC_API_KEY=sk-...
      # - OPENAI_API_KEY=sk-...
```

```bash
docker compose up -d
```

要运行初始设置，使用 `docker compose exec anna anna --open` 或使用 `--port 8080` 启动网关并通过 Web UI 进行配置。

### 本地构建

```bash
# 单平台
docker build -t anna .

# 多平台
docker buildx build --platform linux/amd64,linux/arm64 -t anna .
```

## 使用 Docker 作为沙盒后端

将 anna 运行在 Docker 容器中（见上文）与使用 Docker 作为 agent 工具执行的沙箱后端是两件独立的事。两者可以结合使用（Docker-in-Docker 或挂载 socket），但各自独立也有价值。

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

所有数据都存储在 anna 主目录下（默认为 `~/.anna`，可通过 `ANNA_HOME` 配置）。

| 路径                                    | 用途                             |
| --------------------------------------- | -------------------------------- |
| `~/.anna/anna.db`                       | 单一数据库（配置、记忆、调度器） |
| `~/.anna/workspaces/{agent-id}/skills/` | 每个 agent 安装的技能            |
| `~/.anna/workspaces/{agent-id}/SOUL.md` | 可选的每个 agent 的灵魂/身份覆盖 |
| `~/.anna/cache/`                        | 模型缓存（可重新生成，安全删除） |

`anna.db` 文件是唯一需要备份的关键数据。它包含所有配置、消息历史、摘要和调度器任务。

## 环境变量

配置通过管理面板管理（通过 `anna --open` 或 `--port`）。还支持使用 `HOST` 和 `PORT` 绑定管理面板，其余仅支持少量环境变量：

| 变量                | 必需 | 描述                          |
| ------------------- | ---- | ----------------------------- |
| `ANNA_HOME`         | 否   | Anna 主目录（默认 `~/.anna`） |
| `ANTHROPIC_API_KEY` | 是\* | Anthropic 提供商密钥          |
| `OPENAI_API_KEY`    | 是\* | OpenAI 提供商密钥             |

\* 至少需要一个提供商密钥。API 密钥也可以通过管理面板配置。

## 健康检查

网关会记录日志到标准输出。验证它是否正在运行：

```bash
# 二进制文件
anna  # 日志显示在终端中

# Docker
docker logs anna
```
