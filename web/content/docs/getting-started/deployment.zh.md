---
title: 部署
---

## 安装

### Homebrew（macOS 和 Linux）

```bash
brew install CherryHQ/tap/stella
```

### Linux 软件包（.deb / .rpm）

预构建的安装包可在 [Releases](https://github.com/CherryHQ/stella/releases) 页面获取。`bubblewrap` 已声明为依赖项，会自动安装。

```bash
# Debian / Ubuntu
sudo apt install ./stella_*_linux_amd64.deb

# Fedora / RHEL
sudo dnf install ./stella_*_linux_amd64.rpm
```

### 二进制文件

从 [GitHub Releases](https://github.com/CherryHQ/stella/releases) 下载适用于 linux、macOS 或 Windows（amd64/arm64）的预编译二进制文件，然后将其放置在 `$PATH` 中。

```bash
# 示例：Linux amd64
curl -LO https://github.com/CherryHQ/stella/releases/latest/download/stella_linux_amd64.tar.gz
tar xzf stella_linux_amd64.tar.gz
chmod +x stella
sudo mv stella /usr/local/bin/
```

### Go

```bash
go install github.com/CherryHQ/stella/cmd/stella@latest
go install github.com/CherryHQ/stella/cmd/stellad@latest
# 或
git clone https://github.com/CherryHQ/stella.git
cd stella && go build -o stella ./cmd/stella/ && go build -o stellad ./cmd/stellad/
```

## 运行

启动服务器 —— Web UI访问地址：`http://localhost:25678`：

```bash
stellad server
```

这会启动服务器并提供Web UI，你可以在其中设置 API 密钥、渠道和代理配置。所有配置都存储在 `~/.stella/stella.db` 中 —— 无需手动配置文件。

```bash
stellad server --port 8080                  # 自定义端口
stellad server --host 0.0.0.0 --port 8080   # 绑定所有网络接口
```

### 版本和自动升级

```bash
stella version
stellad upgrade
stellad upgrade --install-dir "$HOME/.local/bin"  # 自定义安装路径
```

`stellad upgrade` 从 GitHub 获取最新稳定版本，下载与当前操作系统/架构匹配的安装包，并默认替换当前正在运行的 `stellad` 二进制文件。如果目标目录不可写，请用具备对应系统权限的用户重新运行，或使用 `--install-dir` 指定其他目录。如果二进制文件被锁定或显示 busy，请先停止正在运行的 Stella 进程或服务，再重试。

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

首先，使用 `--port 8080` 运行 stella 通过Web UI进行配置：

```bash
docker run -it --rm \
  --security-opt seccomp=unconfined \
  -v ~/.stella:/home/nonroot/.stella \
  -p 8080:8080 \
  ghcr.io/cherryhq/stella:latest \
  stellad server --port 8080
```

然后启动服务器：

```bash
docker run -d \
  --name stella \
  --security-opt seccomp=unconfined \
  -v ~/.stella:/home/nonroot/.stella \
  -e ANTHROPIC_API_KEY=sk-... \
  ghcr.io/cherryhq/stella:latest \
  stellad server
```

容器以 `nonroot` 用户运行。挂载 `~/.stella` 以持久化数据库、技能和缓存。您可以设置 `STELLA_HOME` 来更改容器内的数据目录。`--security-opt seccomp=unconfined` 标志是本地沙箱后端（bwrap）在容器内调用 `unshare(2)` 所必需的。

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

要运行初始设置，使用 `--port 8080` 启动服务器，通过 `http://localhost:8080` 的Web UI进行配置，或使用 `docker compose exec stella stellad server --port 8080`。

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

- **Windows**：本地沙箱后端仅支持 Linux/macOS。`docker` 后端通过 Docker Desktop 为 Windows 用户提供真正的隔离边界。
- **自定义工具链**：需要与宿主机不同的特定 Python/Node/Go 版本，或需要干净的 Linux 用户空间。
- **副作用隔离**：需要可复现的文件系统状态，不希望 agent 脚本产生宿主机级别的副作用。

### 权衡取舍

- **启动延迟**：容器热启动约 200ms；首次拉取镜像约 1–3s。
- **绑定挂载性能**：在 macOS/Windows 的 Docker Desktop 上，绑定挂载文件系统操作比原生磁盘慢 5–20 倍。在这些平台上，有大量读写操作的工作流应避免使用 `docker` 后端。
- **无写时复制隔离**：与本地后端（使用 overlayfs）不同，docker 后端不提供基于 overlay 的 COW。失控脚本可能修改或损坏已挂载的工作区。

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

配置通过Web UI管理（默认 `http://localhost:25678`；使用 `--port` 自定义端口）。还支持使用 `HOST` 和 `PORT` 绑定服务，其余仅支持少量环境变量：

| 变量                | 必需 | 描述                                                            |
| ------------------- | ---- | --------------------------------------------------------------- |
| `STELLA_HOME`       | 否   | Stella 主目录（默认 `~/.stella`）                               |
| `ANTHROPIC_API_KEY` | 是\* | Anthropic 提供商密钥                                            |
| `OPENAI_API_KEY`    | 是\* | OpenAI 提供商密钥                                               |
| `STELLA_VAULT_KEY`  | 是†  | 密钥库使用的 age 私钥 —— 密钥管理、OAuth 和 Bearer Token 所必需 |

\* 至少需要一个提供商密钥。API 密钥也可以通过Web UI配置。

† 未设置 `STELLA_VAULT_KEY` 时，密钥库接口返回 `503`，无法签发 OAuth Token，插件密钥也不会被注入。使用 `age-keygen` 生成密钥。

## 健康检查

网关会记录日志到标准输出。验证它是否正在运行：

```bash
# 二进制文件
stella  # 日志显示在终端中

# Docker
docker logs stella
```
