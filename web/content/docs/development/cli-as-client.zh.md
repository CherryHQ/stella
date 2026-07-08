---
title: CLI 与原生 agent 工具
---

> 本节面向为 Stella 贡献代码的开发者。

## 概述

`stellad server` 是唯一会打开 PostgreSQL、写 server 自有文件、抓取 feed、或修改
Stella 状态的进程。人类使用的界面通过 HTTP 调 server；agent 使用原生内置工具，
这些工具在 server 侧以 `authz.Identity` facade 执行。

旧的 sandbox 模式已经移除：sandbox 镜像不再携带 Stella CLI，Stella 也不再向
agent 会话注入 scoped bearer token。agent 不能在 sandbox 内认证到 HTTP API。需要给
agent 能力时，提供原生工具。

```
┌──────────┐         HTTP          ┌──────────────────────┐
│  Web UI  │ ────────────────────▶ │   stellad server     │
└──────────┘                       │  • PostgreSQL        │
┌──────────┐         HTTP          │  • scheduler         │
│HTTP client│ ────────────────────▶ │  • plugin host       │
└──────────┘                       │  • tool handlers     │
┌──────────┐   native tool call    │  • authz.Identity    │
│  Agent   │ ────────────────────▶ │    As-facades        │
└──────────┘                       └──────────────────────┘
```

## 为什么

- **单一事实源**：业务规则只在 server 实现，不重复散落在 CLI、Web UI 和 agent
  代码里。
- **最小权限**：agent 只拿到按角色注册的工具；不再拿通用 bearer token，也没有
  CLI 逃生通道。
- **可审计**：状态变更仍经过 server handler，日志、指标、限流、鉴权都集中在一处。
- **类型化 agent API**：工具 schema 精确描述 agent 能调用什么、参数是否合法。

## 模式

新增 agent 能力时：

1. 实现 server 侧 domain handler 和鉴权检查。
2. 把 agent 界面暴露成原生 tool action，通常在 OpenAPI spec 中用
   `x-agent-tool` 元数据，再由 toolgen 生成胶水代码。
3. 构建工具时使用 `authz.Identity` facade，例如 `identity.AsUser()` 或 domain
   专用等价物；不要接受调用方传入的 subject override。
4. 更新对应 system skill，让 agent 使用工具名和 action 字段，而不是 shell 命令。

`stellad` 子命令仍供 operator 和本地维护使用，但它不是 agent 集成界面。

## 命令禁止做的事

- 通过 `internal/db.OpenDB` 打开 PostgreSQL。
- 构造 domain store，如 `recally.NewStore(db)`。
- 直接读写 `STELLA_HOME` 下 server 自有文件。
- 增加 sandbox 专用鉴权路径，或依赖 agent-scoped bearer token。

在非 server 命令包中 grep `OpenDB`、`sqlc.` 或 `NewFileManager`，结果应当为空。server 启动和刻意的本地维护路径应位于 `cmd/stellad/` 或内部服务包。
