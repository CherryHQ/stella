---
title: Webhook
---

Webhook 是个人 HTTP 调用能力资源，固定关联一个用户和一个 Agent；它不是聊天渠道。所有已认证用户都只能管理自己的 Webhook，管理员也没有跨用户权限。

## 创建 Webhook

1. 打开 **设置 → Webhooks**。
2. 用名称和你有权使用的 Agent 创建 Webhook。
3. 复制创建后显示的 URL。Stella 不会再次显示该 URL。

URL 本身就是凭据。向它发送 `POST` 请求，不要添加 `Authorization` 请求头。

```bash
curl -X POST https://your-host/webhooks/stella_whk_your_capability \
  --data '总结刚完成的部署。'
```

Webhook 始终以所有者身份使用绑定的 Agent 运行。Stella 每次准入都会检查用户和 Agent 是否启用，以及用户是否仍有该 Agent 的权限。Webhook 被停用、权限撤回、用户停用或 Agent 停用后，后续请求都会以不透明的 `404` 失败。

## 调用选项

选项只作用于本次请求，不会保存到 Webhook：

- `?wait=true|false` 为 true 时等待回复，默认 `false`。
- `?session_mode=ephemeral|persistent` 选择新会话或连续会话，默认 `ephemeral`。

重复或无效选项返回 `400`。持久会话按 Webhook 唯一隔离。

## 接收 GitHub 事件

GitHub 只是同一个通用 Webhook 能力的调用方，不需要 GitHub 集成。

1. 在 **设置 → Webhooks** 中创建 Webhook，并复制其一次性显示的 URL。
2. 在 GitHub 仓库打开 **Settings → Webhooks → Add webhook**。
3. 将该 Webhook URL 粘贴到 **Payload URL**，选择 **application/json**，并将 **Secret** 留空。
4. 选择 Agent 应接收的 GitHub 事件并保存。

Stella 仅凭不可猜测的 capability URL 授权请求；它不会认证请求来源是否确为 GitHub，也不校验 `X-Hub-Signature-256`。因此不要为此端点配置 GitHub secret。默认调用为异步且使用临时会话：GitHub 收到 `202` 时 Agent 仍在运行，回复不会回传给 GitHub。

GitHub 初次创建时发送的 `ping` 和之后每个选定事件都会调用 Agent。GitHub 也可能重投事件。Stella 不会对投递去重，包括不会按 `X-GitHub-Delivery` 去重；会产生副作用的 Agent 必须做到幂等。

## 管理能力

- **编辑**可修改名称、绑定、启用状态和服务端超时上限。
- **轮换**需要当前 etag，会立即使旧 URL 失效，并仅返回一次新 URL。
- **删除**撤销资源并使 URL 失效。

稳定读取不会返回 URL 或凭据材料。丢失 URL 时，请轮换 Webhook。

## 限制与错误

Webhook 请求体最多 256 KiB，同步回复文本最多 1 MiB；异步调用不会保留 Agent 输出。Stella 按 Webhook 限制准入和并发运行。同步请求使用等待超时（默认 60 秒，最大 600 秒）；每次运行使用运行超时（默认 300 秒，最大 3600 秒）。

| 状态码 | 含义                                           |
| ------ | ---------------------------------------------- |
| `200`  | 同步回复完成                                   |
| `202`  | 异步调用已接受；Agent 在后台继续运行           |
| `400`  | 调用选项或请求体无效                           |
| `404`  | Webhook 无效、已轮换、已删除、已停用或已无权限 |
| `408`  | 未在读取期限内收到完整请求体                   |
| `413`  | 请求体超过 256 KiB                             |
| `429`  | 达到准入、速率或运行限制                       |
| `502`  | 已准入的 Agent 运行失败                        |
| `503`  | Agent 运行时不可用                             |
| `504`  | 同步回复未在等待超时前完成                     |
