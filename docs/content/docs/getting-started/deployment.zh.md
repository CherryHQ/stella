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
anna onboard
```

这会启动一个本地 Web UI，您可以在其中设置 API 密钥、频道和 agent 配置。所有配置都存储在 `~/.anna/anna.db` 中 —— 无需手动配置文件。

启动网关守护进程：

```bash
anna gateway
```

要在网关运行时同时提供管理面板服务（用于运行时配置更改）：

```bash
anna gateway --admin-port 8080
```

或使用交互式 CLI：

```bash
anna chat
```

### 版本和自动升级

```bash
anna version
anna upgrade
anna upgrade --install-dir "$HOME/.local/bin"
```

`anna upgrade` 从 GitHub 获取最新稳定版本，下载与当前操作系统/架构匹配的安装包，并将二进制文件安装到 `$HOME/.local/bin`（默认位置）。

### Systemd 服务（Linux）

```ini
# /etc/systemd/system/anna.service
[Unit]
Description=anna gateway
After=network.target

[Service]
Type=simple
User=anna
WorkingDirectory=/home/anna
ExecStart=/usr/local/bin/anna gateway --admin-port 8080
Restart=on-failure
RestartSec=5

# API 密钥 —— 所有其他配置都存储在数据库中
Environment=ANTHROPIC_API_KEY=sk-...
Environment=ANNA_HOME=/home/anna/.anna

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable --now anna
```

所有配置（频道、agents、调度器任务）都存储在 `anna.db` 中。使用 `anna onboard` 或管理面板来管理配置。

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
  -v ~/.anna:/home/nonroot/.anna \
  -p 8080:8080 \
  ghcr.io/vaayne/anna:latest \
  anna onboard
```

然后启动网关：

```bash
docker run -d \
  --name anna \
  -v ~/.anna:/home/nonroot/.anna \
  -e ANTHROPIC_API_KEY=sk-... \
  ghcr.io/vaayne/anna:latest
```

容器以 `nonroot` 用户运行。挂载 `~/.anna` 以持久化数据库、技能和缓存。您可以设置 `ANNA_HOME` 来更改容器内的数据目录。

### Docker Compose

```yaml
# docker-compose.yml
services:
  anna:
    image: ghcr.io/vaayne/anna:latest
    restart: unless-stopped
    volumes:
      - ./anna-data:/home/nonroot/.anna
    environment:
      - ANTHROPIC_API_KEY=sk-...
      # - OPENAI_API_KEY=sk-...
```

```bash
docker compose up -d
```

要运行初始设置，使用 `docker compose exec anna anna onboard` 或使用 `--admin-port 8080` 启动网关并通过 Web UI 进行配置。

### 本地构建

```bash
# 单平台
docker build -t anna .

# 多平台
docker buildx build --platform linux/amd64,linux/arm64 -t anna .
```

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

配置通过管理面板管理（通过 `anna onboard` 或 `--admin-port`）。仅支持少量环境变量：

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
anna gateway  # 日志显示在终端中

# Docker
docker logs anna
```
