---
title: MCP 服务器
---

## MCP 服务器的作用

Stella 可以连接外部 [Model Context Protocol](https://modelcontextprotocol.io) 服务器，并把它们的工具暴露给你的智能体。注册一次后，这些工具就会出现在智能体的工具箱中，并以 `mcp__<服务器>__<工具>` 命名，避免与技能或内置工具冲突。

Stella 仅作为基于 HTTP 传输的 MCP **客户端**：

- `streamable_http` — 可流式 HTTP 传输（默认）。
- `sse` — HTTP + Server-Sent Events。

本地 `stdio` 服务器有意不受支持：多租户沙箱绝不会启动本地进程。

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

服务器可能需要 bearer 令牌。用 `--auth bearer --token <令牌>` 传入；令牌会在与注册相同的作用域下**加密存储于保险库**（参见[密钥与凭据](/docs/guides/secrets-and-keys)），绝不会写入注册表。无需认证的服务器使用 `--auth none`（默认），即使未配置保险库也可工作。

> 面向 MCP 服务器的完整交互式 OAuth 授权尚未实现。对于受 OAuth 保护的服务器，请在外部获取令牌并以 bearer 凭据方式存储。

## 管理服务器

使用 CLI。命令帮助是确切参数的权威来源：

```bash
stella mcp --help
stella mcp add --help
```

典型流程：

```bash
# 为当前用户注册一个服务器
stella mcp add github --url https://mcp.example.com/mcp --auth bearer --token "$TOKEN"

# 列出某个作用域下的注册
stella mcp list --scope user

# 按 id 删除
stella mcp remove <id> --scope user
```

同样的操作也可通过 HTTP API 的 `/api/mcp/servers` 使用。
