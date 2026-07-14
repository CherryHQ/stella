---
title: Webhook
---

Webhook 渠道把一个 Agent 变成一个 HTTP 端点，你可以从任何脚本、定时任务或第三方服务触发它。它是**仅入站**的：调用方 POST 一段负载，绑定的 Agent 运行，然后你要么拿到一个异步确认，要么在 HTTP 响应里拿到 Agent 的回复。这里没有机器人、没有聊天窗口，Agent 也无法主动给你发消息——它是为自动化而生，而非对话。

## 前置条件

开始之前，请确保你有：

- 一个正在运行的 Stella 服务（`stellad server`）
- 在 Web UI 中至少配置了一个 AI 服务商（如 Anthropic、OpenAI）
- 一个你想让 webhook 运行的、已启用的 Agent

## 工作原理

1. 管理员在 Web UI 中创建一个 webhook 渠道，并把它绑定到**一个 Agent**。它在创建的那一刻就是启用的。
2. Stella 给你一个接入 URL：`https://your-host/webhooks/<渠道-id>`。
3. 调用方用一个个人访问令牌（PAT）向该 URL 发送 `POST`。令牌所属的用户必须有权运行绑定的 Agent。
4. 请求正文成为 Agent 的消息。Agent **以调用者的身份**运行——使用该用户的工具、记忆和权限。不同调用者请求同一个 URL，各自以自己的身份运行。
5. 根据回复模式，调用方会立即收到 `202 Accepted`，或者等待 Agent 的回复。

## 配置步骤

### 1. 创建个人访问令牌

调用方使用个人访问令牌（PAT）鉴权，与 HTTP API 使用的令牌类型相同。

1. 打开 Web UI：`http://localhost:25678`。
2. 进入 **设置 → 个人访问令牌**。
3. 创建一个令牌，勾选 **`agent:write`** 权限范围，并复制它。令牌只显示一次。

令牌所属的用户必须有权运行 webhook 绑定的 Agent。请像对待密码一样对待令牌。

### 2. 创建 webhook 渠道

创建渠道是管理员操作。

1. 进入 **渠道** 页面，添加一个新渠道。
2. 平台选择 **Webhook**，并给它一个渠道 ID（例如 `deploy-notify`）。
3. 选择 **绑定 Agent**——每次触发时运行的 Agent。
4. 选择 **会话模式**，以及是否 **默认等待回复**（见下文），然后保存。
5. 再次打开该渠道即可复制它的 **接入 URL**。

与聊天机器人不同，webhook 渠道无需重启服务、也无需单独启用——保存后立即生效。任何持有合法 PAT、且其用户有权运行绑定 Agent 的人都可以调用该 URL，各自以自己的身份运行。

### 3. 触发它

```bash
curl -X POST https://your-host/webhooks/deploy-notify \
  -H "Authorization: Bearer stella_pat_your_token_here" \
  -H "Content-Type: text/plain" \
  --data '部署 v1.4.2 已完成。请总结更新日志并发布到 #releases。'
```

整个请求正文会作为消息传给 Agent。发送纯文本、JSON 或任何内容都可以——Agent 会原样接收，并自行决定如何处理。

## 回复模式

每次触发要么是异步（触发即忘），要么是同步（等待回复）。渠道的 **默认等待回复** 设置决定默认行为；调用方可以用 `?wait=` 查询参数按请求覆盖它。

| 模式 | 如何选择                   | 响应                                                           |
| ---- | -------------------------- | -------------------------------------------------------------- |
| 异步 | 默认关闭，或 `?wait=false` | 立即返回 `202 Accepted`，携带 `{ "session_id": "..." }`        |
| 同步 | 默认开启，或 `?wait=true`  | 返回 `200 OK`，携带 `{ "session_id": "...", "output": "..." }` |

对单次调用强制同步：

```bash
curl -X POST 'https://your-host/webhooks/deploy-notify?wait=true' \
  -H "Authorization: Bearer stella_pat_your_token_here" \
  --data '2 + 2 等于几？'
```

在同步模式下，调用方最多等待渠道配置的**等待超时**（默认 60 秒，可按渠道配置，上限 10 分钟）来获取回复。如果超时时 Agent 仍在运行，你会收到 `504 Gateway Timeout`——但运行会在后台继续，且响应里包含 `session_id`，你稍后可以在 Web UI 中查看结果。

`?wait=` 传入 `true`/`false`（或 `1`/`0`）以外的值会被拒绝并返回 `400 Bad Request`。

## 会话模式

- **临时**（默认）：每次触发都开启一个全新会话，不记得之前的调用。适合无状态自动化——每段负载独立处理。
- **持久**：每个调用者在该 webhook 上保留一个长期会话，Agent 会在该调用者的多次调用间累积上下文。不同调用者不共享会话——每个 PAT 用户有自己的会话。适合后续触发需要基于之前触发的场景。

> **持久会话与并发：** 持久会话同一时间只处理一次触发。如果上一次运行还未结束时又有触发到达，该请求会被拒绝并返回 `429 Too Many Requests`（两种回复模式都如此）——请等在途运行结束后重试。临时模式不受影响：每次触发都有自己的会话。

> **持久会话：** 持久模式为每个调用方使用一个长期运行的 Agent runner。Stella 只支持一个服务副本；Helm chart 会强制该拓扑。

## 响应码

| 状态码                  | 含义                                                 |
| ----------------------- | ---------------------------------------------------- |
| `200 OK`                | 同步运行完成；正文携带 `output`                      |
| `202 Accepted`          | 异步运行已启动；正文携带 `session_id`                |
| `400 Bad Request`       | 正文为空，或 `?wait=` 值非法                         |
| `401 Unauthorized`      | 令牌缺失、无效或不是 PAT                             |
| `403 Forbidden`         | 令牌缺少 `agent:write`，或其用户无权运行绑定的 Agent |
| `404 Not Found`         | 没有该 ID 的 webhook                                 |
| `409 Conflict`          | webhook 被禁用、未绑定 Agent，或绑定 Agent 缺失/禁用 |
| `413 Payload Too Large` | 正文超过 256 KiB                                     |
| `429 Too Many Requests` | 触发超出速率限制，或持久会话正被另一次运行占用       |
| `500 Internal Error`    | webhook 配置无效，或创建会话失败                     |
| `502 Bad Gateway`       | Agent 运行失败                                       |
| `503 Unavailable`       | 绑定 Agent 的运行时不可用                            |
| `504 Gateway Timeout`   | 同步等待超时；运行在后台继续                         |

## 限制

- **负载大小：** 每次请求最多 256 KiB。
- **速率限制：** 每个 webhook 允许短时突发，之后稳定放行；持续洪泛会返回 `429`。
- **并发上限：** 单个 webhook 同时最多 10 次运行在途；超出的触发会返回 `429`，直到有运行结束。
- **会话记录：** 临时模式下每次触发都会新建一个会话，高频 webhook 会随时间累积会话记录。如果列表变得杂乱，可在 Web UI 中清理旧会话。

## 故障排查

**收到 `401 Unauthorized`？**

- 确认 `Authorization` 头是 `Bearer <令牌>`，且令牌是个人访问令牌（以 `stella_pat_` 开头），而不是 OAuth 或会话令牌。

**收到 `403 Forbidden`？**

- 令牌必须携带 `agent:write` 权限范围。如果忘了勾选，请在 **设置 → 个人访问令牌** 中重新创建。
- 令牌所属的用户必须有权运行绑定的 Agent。请给该用户授予该 Agent 的访问权，或改用一个已有权限的用户的令牌。

**收到 `404 Not Found`？**

- 检查 URL 中的渠道 ID。webhook 必须存在且类型为 Webhook。

**收到 `409 Conflict`？**

- webhook 或它绑定的 Agent 被禁用。请在各自页面启用两者。

**同步调用返回 `504`？**

- Agent 耗时超过了等待超时。对于长耗时任务，请使用异步模式（`?wait=false`），并用返回的 `session_id` 在 Web UI 中查看运行结果。
