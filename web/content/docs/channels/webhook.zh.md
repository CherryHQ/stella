---
title: Webhook
---

Webhook 渠道为一个已配置的自动化场景提供独立 HTTP 端点。它仅接收入站请求：每次请求都以固定所有者的身份运行该渠道绑定的 Agent。它不使用个人访问令牌（PAT），请求也无法选择其他用户、Agent、记忆或工具凭据。

## 开始前

你需要正在运行的 Stella 服务、一个已启用的 Agent 和管理员账号。请谨慎选择所有者：端点会使用该用户的上下文和绑定 Agent 的工具运行。

## 创建通用 Webhook

1. 在 Web UI 创建一个 **Webhook** 渠道，并绑定一个已启用的 Agent。
2. 打开该渠道，选择 **启用 Webhook 端点**。
3. 选择一个活跃所有者和 **通用** 提供方。
4. 在确认对话框中保存 URL。Stella 只显示一次。

该 URL 是不可猜测的 bearer capability。不要添加 `Authorization` 请求头或 PAT；Stella 会忽略它们，且不会让它们影响 webhook 身份。

```bash
curl -X POST 'https://your-host/webhooks/stella_whk_…' \
  -H 'Content-Type: text/plain' \
  --data '部署 v1.4.2 已完成。请总结更新日志。'
```

请求正文会成为 Agent 的消息。所有负载都应视为不可信的外部输入，包括来自你自己系统的 JSON。

## 接入 GitHub

GitHub 端点同时需要 capability URL 和独立的 GitHub 签名密钥。

1. 创建或打开一个绑定了专用、最小权限 Agent 的 Webhook 渠道。
2. 选择 **启用 Webhook 端点**，选择所有者，再选择 **GitHub**。
3. 填写 GitHub 事件和仓库允许列表。只允许此自动化实际需要的事件和 `owner/repository` 名称。
4. 保存一次性显示的 **Webhook URL** 和 **GitHub 密钥**。
5. 在仓库中打开 **Settings → Webhooks → Add webhook**：
   - 将 **Payload URL** 设置为 Webhook URL。
   - 将 **Content type** 设置为 `application/json`。
   - 将 **Secret** 设置为 GitHub 密钥。
   - 只选择渠道允许列表中的事件。
6. 发送测试投递，确认收到 `202 Accepted`。

GitHub 投递必须包含有效的 `X-Hub-Signature-256`、事件和投递 ID。Stella 会在启动任何 Agent 工作前，基于原始请求正文验证签名。GitHub 请求始终异步：`?wait=true` 会被拒绝，Agent 输出绝不会返回给 GitHub。

## 轮换或撤销

当 URL 或 GitHub 密钥可能泄露时，使用 **轮换**。轮换会立刻使旧 URL 失效；对于 GitHub，还会签发新的签名密钥。在使用新端点前，请先更新外部服务。

使用 **撤销** 可立即停止所有请求。之后你可以变更渠道绑定的 Agent 或类型，再启用新端点。端点处于活跃状态时，Stella 会阻止这些变更，避免泄露的 URL 获得不同身份。

Stella 不会再次显示已签发的 URL 或 GitHub 密钥。丢失任一值时，请轮换。

## 回复与会话模式

通用 Webhook 保留渠道的回复和会话设置：

| 模式                              | 响应                        |
| --------------------------------- | --------------------------- |
| 异步（默认，或 `?wait=false`）    | `202 Accepted`，附带会话 ID |
| 同步（默认开启，或 `?wait=true`） | `200 OK`，附带 Agent 输出   |

同步请求可能返回 `504`，但运行仍会继续。持久通用会话同一时间只接受一个运行；会话繁忙时返回 `429` 和 `Retry-After: 1`。

GitHub 端点对有效、已接受或重复的投递始终返回空的 `202 Accepted`。无效签名返回 `401`；签名正确但事件或仓库不在允许列表内时，返回 `202`，且不会运行 Agent。

## 去重与重试

Stella 会保留 GitHub 投递 ID **30 天**。同一端点重复的投递 ID 返回 `202`，不会再次运行 Agent。30 天后，手动重新投递可能再次运行。

如果 Stella 在 Agent 接纳运行前拒绝投递，会释放该投递声明并返回可重试的非 2xx 响应，GitHub 可以重试。一旦 Agent 已接纳投递，即使后续工作失败，Stella 仍保留声明：在工具可能已经产生部分副作用后重放 Agent，比停止自动重放更不安全。

GitHub 自动化仍应保持幂等。投递可能在 30 天窗口后重试，外部系统也可能多次发送相关事件。

## 限制与响应码

- 请求正文上限：256 KiB。
- 每个端点有速率限制，且最多允许 10 个在途运行。超出时返回 `429`。
- `404 Not Found` 表示 capability 格式错误、不存在、已撤销、已禁用，或其所有者或 Agent 不再活跃。Stella 刻意不会说明具体原因。
- `503 Service Unavailable` 表示 Agent 未能接纳运行，请稍后重试。

## 最小权限

尽可能为 webhook 创建独立 Agent。只给它此自动化所需的工具、GitHub 凭据、仓库和指令，并选择同样采用最小权限的所有者。有效签名只能证明负载来自 GitHub；它不能让 issue 文本、Pull Request 内容或其他负载字段变成可信指令。
