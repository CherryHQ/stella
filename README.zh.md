<p align="center">
  <img src="avatar.png" width="200" alt="stella" />
</p>

# Stella — 面向个人、团队和日常工作的 AI 伙伴

> **⚠️ 正在快速开发中** — Stella 还不稳定。API、配置格式和行为都可能变化。不建议用于生产环境。

Stella 不只是一个聊天 agent。她是一个可以长期进入公司、家庭或个人工作流的 AI 伙伴：记住项目上下文的同事，持续推动日常事务的家庭管家，或了解你工作方式的私人助手。

Stella 天生是多租户、多用户、多 agent 系统。每个 agent 都可以有自己的角色、工具、技能、定时任务、沙箱策略和工作区。每个用户和每个 agent 之间都有专属记忆，因此同一个写作伙伴、编程 agent 或家庭助手可以理解不同的人，而不是把所有人压成同一份画像。

你可以把 Stella 部署在自己选择的环境里，使用自己的模型 API 密钥，并从 Telegram、QQ、飞书、微信、Web UI 或终端与她对话。对话、密钥、记忆和工作区都由你掌控。

## 为什么使用 Stella

- **伙伴，而不是另一个聊天框。** Stella 面向持续关系和长期工作设计：公司同事、家庭管家、私人助手、研究伙伴或编程协作者。
- **天生多租户、多用户。** Stella 支持同一套部署服务多个用户。记忆按用户和 agent 隔离，因此每个人都能和每个 agent 建立专属关系。
- **多个 agent，多个角色。** 创建编程 agent、写作伙伴、运营助手、家庭日程管家或阅读研究员。每个 agent 都有自己的性格、模型、技能、工具、定时任务、工作区和沙箱策略。
- **内置工作区和沙箱。** agent 可以在专属工作区里处理文件和工具，沙箱后端负责控制执行边界和网络访问。
- **出现在每个常用渠道。** 从 Telegram 开始，在 Web UI 继续，从终端提问，或在微信、QQ、飞书里接收通知。合适的 agent 可以出现在每个用户已经使用的地方。
- **有来源的记忆。** Stella 使用 Lossless Context Management (LCM) 压缩旧对话，同时保留原始内容可搜索。需要时，她可以回到某段记忆背后的准确上下文。
- **持续运行的日常事务。** 设置提醒、周期性任务、阅读摘要和后台任务；它们可以跨重启保留，并通过已连接渠道通知相关人员。

## 快速开始

```bash
# 1. 安装
brew install CherryHQ/tap/stella

# 2. 设置 API 密钥
export ANTHROPIC_API_KEY="sk-ant-..."

# 3. 启动服务器
stella server

# 4. 打开 Web UI：http://localhost:25678
#    在 Providers 中添加模型提供商和 API 密钥

# 5. 打开 Chat，开始对话
```

你也可以使用 `go install github.com/CherryHQ/stella@latest` 安装，或从 [Releases](https://github.com/CherryHQ/stella/releases) 下载二进制文件。

详见[完整快速开始指南](web/content/docs/getting-started/quickstart.zh.md)。

## 连接聊天渠道

所有渠道共享同一套记忆。你可以从一个渠道开始，再切换到另一个渠道，Stella 会接上之前的上下文。

| 渠道     | 连接方式               | 流式响应支持   |
| -------- | ---------------------- | -------------- |
| Terminal | 内置 TUI               | Token-by-token |
| Telegram | 长轮询，无需公网 IP    | 支持           |
| QQ       | WebSocket              | 支持           |
| 飞书     | WebSocket，无需公网 IP | Edit-in-place  |
| 微信     | 长轮询（iLink Bot）    | 不支持         |

你可以把某个渠道绑定到特定 agent，也可以让用户通过 Telegram 的 `/agent` 切换 agent。

## 技能

通过 CLI 搜索、安装和管理技能：

```bash
stella skill search "web scraping"
stella skill install owner/repo@skill-name
stella skill list
```

## 文档

| 分区 | 内容                         | 链接                                         |
| ---- | ---------------------------- | -------------------------------------------- |
| 入门 | 安装、部署、配置             | [快速开始](/docs/getting-started/quickstart) |
| 指南 | 记忆、定时任务、技能、通知   | [指南](/docs/guides/memory)                  |
| 渠道 | Telegram、QQ、飞书、微信配置 | [渠道](/docs/channels/telegram)              |
| 开发 | 架构、插件、贡献             | [开发](/docs/development/architecture)       |

## CLI 参考

```bash
stella server                           # 启动服务器；Web UI 位于 http://localhost:25678
stella server --port 8080               # 自定义端口
stella skill search <query>             # 搜索 skills.sh
stella skill install <name>             # 安装技能
stella skill list                       # 列出已安装技能
stella scheduler list                   # 列出定时任务
stella vault list                       # 列出已保存的密钥
stella version                          # 打印版本
stella upgrade                          # 自升级到最新版本
```

## 开发

开发需要 [mise](https://mise.jdx.dev/)。全新克隆后：

```bash
mise run setup    # 设置开发环境和 pre-commit hooks
mise run build    # 构建二进制文件
mise run test     # 运行测试
mise run format   # Lint 和格式化
```

## 许可证

MIT
