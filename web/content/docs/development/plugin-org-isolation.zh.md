---
title: 插件按 Org 隔离
description: Stella 如何把插件运行时、manifest override、OAuth provider 配置和 channel 回调严格归属到单个 org。
---

## 概览

Stella 是多租户系统。每个插件运行时、manifest override、OAuth provider 配置、channel 回调必须精确归属到一个 org。本文记录了实现这一保证的契约。

## Org context 契约

Org 标识随 `context.Context` 流动，helper 在 `internal/orgctx`：

```go
ctx = orgctx.WithOrgID(ctx, orgID)
got := orgctx.OrgIDFromContext(ctx) // 缺失时返回 ""
```

两层执行这个契约：

- **HTTP 中间件** 给每个 `/api/*` handler 的 `r.Context()` 注入已登录用户的 `orgID`。
- **Channel runtime**（每 `(org, channel.ID, runtimeName)` 一个实例）在 Build 时用 `pkgchannel.HandlerWithOrgID(orgID, inner)` 包装共享的 coordinator handler。所有 SDK 回调（`onMessage`、`onReaction`、telegram poller 等）经过这层 wrapper 后，coordinator 看到的 ctx 一定带正确的 org，即便 SDK 本身只给一个裸 context。

下游代码（`internal/store/dbstore.go`、`internal/pluginhost/runtime.go`、`internal/channel/coordinator.go`）用 `requireOrgID(ctx)` 在缺 org 时显式失败。读路径返回 `(nil, false)`；写路径返回 error。

## RuntimeHost 按 org 分桶

`internal/pluginhost/runtime.go` 用两层 map 存运行时：

```go
rt map[string]map[runtimeKey]*runtimeEntry  // orgID -> {RuntimeID, RuntimeName} -> entry
```

两个 org 都配置了名为 `feishu-main` 的 channel？**各自拥有独立的 runtime 实例和 SDK 客户端**，互不影响。`runtimeKey` 是 struct 不是字符串拼接，避免分隔符转义碰撞。

`Host.Shutdown(ctx)` 只清除当前 org 的桶；`Host.Stop(ctx)` 留给进程退出时遍历所有桶。

## Manifest 默认值 + per-org override

打进二进制的 manifest（`resources.BuiltinPluginsYAML()`）是插件默认值的**唯一来源**。per-tenant override 存在 `settings_manifest_plugin_override`（PK = `(plugin_id, org_id)`）：

| 列                      | 语义                                                              |
| ----------------------- | ----------------------------------------------------------------- |
| `plugin_id`             | manifest 插件 ID                                                  |
| `org_id`                | 租户                                                              |
| `enabled`               | nullable；`NULL` = 用 manifest 默认，非 `NULL` = 显式 override    |
| `session_env_vault_key` | 空 = fallback 默认，非空 = vault 中存的 session_env override 映射 |

Override 是稀疏存储：只有真正偏离默认值的字段才会写 DB。`SaveManifestPlugins` 仅在**没有任何东西需要 override 时**才删除该行——即请求的 `enabled` 与 manifest 默认一致**且**没有 `session_env_vault_key` 绑定。若存在 session env 绑定，则保留该行,并把 `enabled` 存为 `NULL`,使其仍回退到默认。这样既保持 DB 最小差异，又不会抹掉 session env 维度。

读路径走 `Server.resolveManifestPlugins(r)`，把 `ListManifestPluginOverrides` 叠加到 builtin manifest 上后返回。builtin manifest 本身永远不可变。

`Reconcile`（二进制 / skill 安装）装到 `$STELLA_HOME/bin` 和系统 skill 目录——**系统级资源，没有 org 维度**。`SyncManifestPlugins` 确实会把已叠加 override 的 manifest 传给 `Reconcile`，所以 per-org 启用状态会到达它,但安装层（`StellaHome`、bin 目录、reconcile 状态文件）在所有 org 间共享。per-org 启用/禁用只决定 agent runtime 是否调用该插件,不决定安装哪些二进制。让安装层 org 化由单独的 issue 跟踪（见 issue #244）。

## OAuth provider per-org override

`plugin_oauth_provider` 表存 per-org OAuth client override：

- `client_id`、`client_secret_enc`（vault 加密）、`redirect_url`、`org_id`
- 通过 `PUT /api/admin/oauth-providers/{id}/config` 更新
- `credentials.Service.GetEffectiveOAuthConfig(ctx, providerID)` 把 DB override 合并到 manifest YAML 默认上
- `GET` 响应把 secret redact 成 `"***"`

OAuth 流的 `state` 参数绑定了 `orgID`（`oauth.FlowStatus.OrgID`）；`CompleteAuthCodeFlowWithOrigin` 在 token exchange 前把 orgID 注入 ctx，保证 callback 始终在正确 org 下执行。

## 已移除的内容

- `~/.stella/plugins.yaml` 与 `manifestplugins.LoadUser` / `Merge` / `MergeRaw` 加载器。per-tenant 配置一律走 REST API → DB。
- MCP 插件（`internal/tools/mcp`）。它依赖的运行时模型和 `RegisterBuiltinTools` 都已删除。DB 中遗留的 `settings_plugin` 中 `id='tool/mcp'` 的行是惰性的——没有任何插件注册在该 ID 下,因此永远不会解析成 runtime。

## 从旧 plugins.yaml 迁移

如果你之前的 fork 写过 `~/.stella/plugins.yaml`：

1. 该文件启动时被忽略；不报错也不迁移。
2. 通过 Web UI 的 Plugins 页面（每个 org 单独操作）重新设置 per-tenant override，或通过 `PUT /api/manifest-plugins` 编程式写入。
3. 迁移完成后删掉这个旧文件。
