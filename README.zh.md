<p align="center">
  <img src="avatar.png" width="200" alt="stella" />
</p>

<p align="center">
  <a href="README.md">English</a> | 中文
</p>

# Stella — 为每个团队打造共享的 AI 同事

> **⚠️ 正在快速开发中** — Stella 还不稳定。API、配置格式和行为都可能变化。不建议用于生产环境。

Stella 把团队里重复的专业能力——财务、HR、工程、研究——变成共享的 AI 同事。配置一次，其他人就能在自己已经在用的聊天工具里直接问它。

业务负责人为 agent 配置 instructions、skills、tools、knowledge 和记忆策略。配置好之后，谁都不必再为了推进工作去学财务系统、招聘工具或内部工具链——把目标告诉 agent，它就会在你设定的边界内把事做完。每个人和 agent 之间都有独立的记忆，因此 Stella 理解不同的同事，而不是把所有人压成同一份画像。

在底层，Stella 是一个多租户、多用户、多 agent 系统，但它带给你的价值很简单：很多人可以同时依赖同一个 agent，每个 agent 都有自己的角色、模型、技能、工具、定时任务、工作区和安全边界，而控制权始终在你手里。你可以把它部署在自己选择的环境里，使用自己的模型 API 密钥，并从 Telegram、Discord、QQ、飞书、钉钉、微信、Web UI 或终端访问它。

小团队和独立开发者也可以用同一套配置——让一个 agent 去做没人有空做的后台工作——但 Stella 首先是为那些反复花钱让专家回答同样问题的团队打造的。

## 为什么使用 Stella

- **谁都能直接问。** 同事不必为了得到帮助去学财务或 HR 系统——在聊天里问 agent，它就会把事做完。
- **一个专家，全员共享。** 业务负责人把 agent 配置一次；整个团队复用它，而不是去打扰那个专家。
- **记住每个同事。** 记忆按用户和 agent 隔离，因此谁都不必重新解释自己的上下文。
- **在你设定的边界内行动。** agent 在专属工作区、沙箱策略和受控工具权限下做事，并在你要求的地方停下来等人工审核。
- **就在你已经用的聊天里。** Telegram、Discord、QQ、飞书、钉钉、微信、Web UI 和终端，都是同一套 agent 的入口。
- **持续推动日常事务。** 设置提醒、周期性任务、阅读摘要和后台任务；它们可以跨重启保留，并通知正确的人。

## 快速开始

```bash
# 1. 安装
brew install CherryHQ/tap/stella

# 2. 启动服务器
stellad server

# 3. 打开 Web UI：http://localhost:25678
#    在 Providers 中添加模型提供商和 API 密钥

# 4. 打开 Chat，开始对话
```

你也可以从 [Releases](https://github.com/CherryHQ/stella/releases) 下载二进制文件，或用 `git clone` 加 `mise run build` 从源码构建。不支持 `go install`：二进制内嵌了生成代码、Web UI 和内置运行时，这些都不在版本控制里。

详见[完整快速开始指南](web/content/docs/getting-started/quickstart.zh.md)。

## 连接聊天渠道

所有渠道共享同一套记忆。你可以从一个渠道开始，再切换到另一个渠道，Stella 会接上之前的上下文。

| 渠道     | 连接方式                 | 流式响应支持   |
| -------- | ------------------------ | -------------- |
| Terminal | 内置 TUI                 | Token-by-token |
| Telegram | 长轮询，无需公网 IP      | 支持           |
| Discord  | Gateway WebSocket        | 最终响应       |
| QQ       | WebSocket                | 支持           |
| 飞书     | WebSocket，无需公网 IP   | Edit-in-place  |
| 钉钉     | Stream 模式，无需公网 IP | 最终响应       |
| 微信     | 长轮询（iLink Bot）      | 不支持         |

你可以在 Web UI 中把每个渠道绑定到特定 agent。

## MCP 工具

Stella 通过 streamable HTTP 或 Server-Sent Events 连接远程 MCP（Model Context
Protocol）服务器。MCP 声明直接保存在普通文件中：独立服务器使用
`mcp/<name>.json`，包内服务器跟随所属完整包保存。文件只保存凭据引用，认证
令牌独立加密保存在授权存储中。

资源有四种作用域：`system`、`system_agent`、`user` 和 `user_agent`。同一个
完整包按 `user_agent` > `user` > `system_agent` > `system` 选择；更窄作用域
会整体替换更宽作用域的包。复制出来的包独立存在，不会随来源后续修改自动升级。
文件编辑在下一轮生效，已经开始的 turn 继续使用本轮捕获的内容。删除声明只删除
资源，不会断开 OAuth 授权；需要撤销本地访问时必须单独执行 **Disconnect**。Disconnect
会关闭本地连接并阻止迟到刷新，但不承诺远端 provider 一定撤权。资源字节和安装缓存会
保留到经过验证的维护操作安全清理为止。local backend 会执行沙箱策略，但不能证明脱离
进程组的后代已停止；`none` sandbox 不提供可靠的进程隔离。

## 技能

Skill 是教 Stella 执行特定任务的可复用操作手册。Skill 可以直接存在项目、Agent、
用户或系统资源根，也可以存在完整包中。Stella 先选择胜出的完整包，再一起读取其中
的 Skill、CLI、环境绑定和 MCP 声明。更窄作用域会整体替换更宽作用域；复制的包独立
存在，不会自动跟随来源升级。文件修改在下一轮生效，已经接纳的 turn 保留本轮视图。

Web UI 和 API 可以在选定作用域编辑完整包，也可以编辑独立 Skill 或 MCP 文件。
`settings.json` 只负责停用资源和施加管理员限制，不会改变声明本身。OAuth 的
Disconnect 与删除文件是两个动作。无法证明相关进程及其后代已经停止时，Stella
会保留资源字节。`none` backend 不提供可靠的进程隔离。作用域、优先级和编辑方式
详见[技能指南](web/content/docs/guides/skills.zh.md)。

## 文档

| 分区    | 内容                                    | 链接                                         |
| ------- | --------------------------------------- | -------------------------------------------- |
| 入门    | 安装、部署、配置                        | [快速开始](/docs/getting-started/quickstart) |
| 指南    | 记忆、定时任务、技能、通知              | [指南](/docs/guides/memory)                  |
| 渠道    | Telegram、Discord、QQ、飞书、钉钉、微信 | [渠道](/docs/channels/telegram)              |
| Webhook | 个人 HTTP 调用能力                      | [Webhook](/docs/webhooks/webhook)            |
| 管理    | Kubernetes sandbox（本地开发）          | [Kubernetes](/docs/admin/kubernetes)         |
| 开发    | 架构、插件、贡献                        | [开发](/docs/development/architecture)       |

## CLI 参考

```bash
stellad server                          # 启动服务器；Web UI 位于 http://localhost:25678
stellad server --port 8080              # 自定义端口
stellad upgrade                         # 自升级到最新版本
stellad version                         # 打印版本
stellad vault keygen                    # 生成保险库引导密钥
stellad system-bundle revision          # 打印 builtin Skill bundle 版本
stellad system-bundle install           # 安装已验证的 builtin Skill bundle
stellad system-bundle verify            # 验证 builtin Skill bundle
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

GNU Affero General Public License v3.0 或更高版本。详见 [LICENSE](LICENSE)。
