---
title: CLI 作为 REST 客户端
---

> 本节面向为 Stella 贡献代码的开发者。

## 概述

stella CLI 被有意做成轻量 REST 客户端。运行中的 `stellad server` 进程是**唯一**会
打开 SQLite 数据库、写 Markdown 库、抓 RSS 订阅、或者做任何状态变更的进程。

这是项目的 **API-first** 原则：每个能力首先通过 HTTP 暴露，CLI 只是众多客户端
之一（CLI、Web UI、未来的 SDK 与外部集成都消费同一份契约）。

```
┌──────────┐         HTTP          ┌──────────────────────┐
│   CLI    │ ────────────────────▶ │   stellad server       │
│ (stella …) │  Bearer STELLA_TOKEN    │  • SQLite            │
└──────────┘                       │  • Markdown 库       │
                                   │  • RSS 抓取          │
┌──────────┐                       │  • 调度器            │
│   Web    │ ────────────────────▶ │  • 插件宿主          │
└──────────┘                       └──────────────────────┘
┌──────────┐
│   SDK    │ ────────────────────▶
└──────────┘
```

## 为什么

- **单一事实源**：业务规则只在 server 实现，不重复散落在 CLI/Web/SDK。两个
  写库者会在 schema 变更时撞车；只有一个就不会。
- **天然支持远端**：`STELLA_SERVER_URL=https://stella.example.com stella recally list`
  与本地用法走同一份代码。
- **可审计**：所有写操作都经过 HTTP，日志、指标、限流、鉴权都集中在一处。
- **类型安全**：OpenAPI spec 是契约。server interface 与 client 之间的漂移在
  代码生成 / 编译期就能发现，不会拖到运行时。

## 模式

每个 domain（recally 是首个；后续 scheduler / skills / tools 跟进）包含：

1. `api/<domain>.openapi.yaml`（OpenAPI 3.0）—— **契约源头**，spec 改了
   再回头生成代码，不允许反向。
2. 生成的 server interface `internal/server/<domain>_gen.go`，对应实现
   `internal/server/<domain>_handlers.go`。
3. 生成的 client `pkg/<domain>/client/client_gen.go`，外加一个小 `auth.go`
   读取环境变量 `STELLA_TOKEN` / `STELLA_SERVER_URL`。
4. CLI 子命令 `cmd/stella/<domain>*.go`，从 flag 构造类型化请求、调用生成的
   client、解码 JSON、打印输出。

通过 mise 重新生成：

```bash
mise run generate:api
```

## 新增一个 domain

以 `notes` 为例：

1. 写 `api/notes.openapi.yaml`，挑标准 REST URL（`/api/notes`、
   `/api/notes/{id}`），错误响应复用现有 `Error` 形状。
2. 把 codegen 命令加进 `mise run generate:api`（`mise.toml`）。
3. 跑 `mise run generate:api` 产出 `internal/server/notes_gen.go` 与
   `pkg/notes/client/client_gen.go`。
4. 在 `internal/server/notes_handlers.go` 实现生成的 `ServerInterface`。
5. 在 `internal/server/routes.go` 接线：
   `s.registerNotesRoutes()` → `HandlerFromMux(s.notes, s.mux)`。
6. 在 `internal/server/server.go` 注入新 domain 的 store。
7. 把 `cmd/stella/notes*.go` 里直连 DB 的代码替换为
   `notesclient.NewFromEnv()`。

## Bearer token 鉴权

CLI 读取 `STELLA_TOKEN` 并以 `Authorization: Bearer …` 发送。server 的
`authMiddleware`（`internal/server/middleware.go`）已经通过
`authInfoFromBearer` 处理了 bearer，因此新 domain 的路由只要挂到 `s.mux`
上就自动有鉴权。

Bearer token 鉴权要求 server 启动时设置了 `STELLA_VAULT_KEY`（token 材料经 vault
加密存储）。

## CLI 命令禁止做的事

- 通过 `internal/db.OpenDB` 打开 SQLite
- 构造 domain store，如 `recally.NewStore(db)`
- 读写 `STELLA_HOME/library/...` 下的文件
- 替用户去拉外部资源（RSS 订阅、网页等）—— 这件事归 server，CLI 通过动作
  端点（`POST /api/recally/feeds/{id}/poll`）触发

在 `cmd/stella/` 下 grep `OpenDB` / `sqlc.` / `NewFileManager`，结果应当只剩
server 启动相关代码（`gateway.go`）。
