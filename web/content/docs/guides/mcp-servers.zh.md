---
title: MCP 服务器
---

## MCP 服务器的作用

Stella 可以连接外部 [Model Context Protocol](https://modelcontextprotocol.io) 服务器，并把它们的工具暴露给你的智能体。注册一次后，这些工具就会出现在智能体的工具箱中，并以 `mcp__<服务器>__<工具>` 命名，避免与技能或内置工具冲突。

Stella 仅作为基于 HTTP 传输的 MCP **客户端**：

- `streamable_http` — 可流式 HTTP 传输（默认）。
- `sse` — HTTP + Server-Sent Events。

本地 `stdio` 服务器有意不受支持：多用户沙箱绝不会启动本地进程。

端点必须解析到公网地址。回环与私网 URL 会被拒绝，除非运维以 `STELLA_MCP_ALLOW_PRIVATE_ENDPOINTS=1` 启动 `stellad`，该开关面向本地开发服务器。

## 作用域

注册使用与技能和保险库相同的四种作用域，因此服务器可以共享或私有：

| 作用域         | 可见范围                   |
| -------------- | -------------------------- |
| `system`       | 所有智能体、所有用户       |
| `system_agent` | 某个智能体，跨所有用户     |
| `user`         | 某个用户，跨其所有智能体   |
| `user_agent`   | 某个用户搭配某个特定智能体 |

当两个注册同名时，最具体者优先：`user_agent` > `user` > `system_agent` > `system`。

## 认证

服务器可能需要 bearer 令牌。在 Web UI 中创建或编辑注册时配置它，或在 `POST /api/mcp/servers` 的请求体中传入 `token`；令牌会在与注册相同的作用域下**加密存储于保险库**（参见[密钥与凭据](/docs/guides/secrets-and-keys)），绝不会写入注册表。无需认证的服务器不需要凭据，即使未配置保险库也可使用。

> 面向 MCP 服务器的完整交互式 OAuth 授权尚未实现。对于受 OAuth 保护的服务器，请在外部获取令牌并以 bearer 凭据方式存储。

## 状态与探测

Stella 会探测每台已注册的服务器——建立连接并获取其工具列表——并把结果记录在注册上：

| 状态         | 含义                                           |
| ------------ | ---------------------------------------------- |
| `unknown`    | 尚未探测                                       |
| `ok`         | 上次探测成功连接并列出了工具                   |
| `error`      | 上次探测或工具调用失败；界面会显示脱敏后的原因 |
| `needs_auth` | 服务器以 401/403 拒绝了已存储的凭据            |

探测会在以下时机自动运行：创建服务器时、其 URL、传输方式或认证方式变更时，以及智能体会话需要工具列表而上次快照已超过 24 小时时。你也可以随时在 Web UI 中点击 **Probe**，或通过 API 调用 `POST /api/mcp/servers/{id}/probe` 手动触发。探测失败不会破坏任何东西——它只会更新状态，让你能在界面中看到问题（以及脱敏后的原因）。

当工具调用被 401/403 拒绝时，服务器会转为 `needs_auth`；在 Web UI 中更新凭据后重新探测即可。

## 管理服务器

在 **个人设置 → MCP 服务器** 管理个人的 `user` 与 `user_agent` 注册。管理员在 **管理控制台 → 部署资源 → 全局 MCP** 管理部署所有的 `system` 与 `system_agent` 注册。添加服务器 URL，选择适用于全部智能体或单个智能体；如果服务器需要认证，再提供 bearer 令牌。MCP 没有独立的管理 CLI：管理通过 Web UI、`/api/mcp/servers` 下的 HTTP API，或智能体的 `settings_mcp_server_*` 工具进行。
