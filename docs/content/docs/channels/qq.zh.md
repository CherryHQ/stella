---
title: QQ 机器人
---

anna 包含一个通过 WebSocket 连接的 QQ 机器人（持久连接，无需公网 URL）。

## 设置

1. 在 [QQ 开放平台](https://q.qq.com/) 注册一个 QQ 机器人并获取你的 AppID 和 AppSecret
2. 运行 `anna onboard` 启动管理面板
3. 在管理面板中：添加一个 AI 提供商，然后使用你的 AppID 和 AppSecret 配置 QQ 频道
4. 启动网关：

```bash
anna gateway
```

所有频道配置（凭据、群组模式、允许的 ID 等）都通过管理面板管理。环境变量仅限于提供商 API 密钥（`ANTHROPIC_API_KEY`、`OPENAI_API_KEY`）和 `ANNA_HOME`。

## 多用户支持

每个 QQ 用户会从其平台身份自动解析。会话按用户和代理分别管理。无需手动设置用户。QQ 频道当前使用默认代理（`/agent` 命令暂未支持 QQ）。

## 流式响应

机器人使用 QQ 的原生 Stream API 进行渐进式响应传递。随着 LLM 生成令牌，更新会实时发送，无需编辑之前的消息。

### 工具指示器

在工具执行期间，流式消息会显示带有表情符号的状态指示器：

| 工具     | 表情符号 |
| -------- | -------- |
| `bash`   | 闪电     |
| `read`   | 书本     |
| `write`  | 铅笔     |
| `edit`   | 扳手     |
| `search` | 放大镜   |

## 群组支持

QQ 群组消息作为 @提及事件（`GROUP_AT_MESSAGE_CREATE`）接收。在管理面板中设置群组模式：

- `mention` -- 响应 @提及（默认）
- `always` -- 对于 QQ 与 mention 相同（AT 事件始终是提及）
- `disabled` -- 完全忽略群组消息

## 访问控制

通过在管理面板中添加允许的 OpenID 来限制哪些 QQ 用户可以与机器人交互。留空则允许所有用户。使用 `/whoami` 命令获取你的 OpenID。

## 图片支持

用户可以向机器人发送图片进行分析。机器人会下载图片附件，对其进行编码，并将其作为多模态内容与任何说明文本一起传递给 AI 模型。

## 命令

将这些命令作为文本消息发送给机器人：

| 命令                | 描述                     |
| ------------------- | ------------------------ |
| `/start` 或 `/help` | 欢迎和帮助信息           |
| `/new`              | 开始新的会话             |
| `/compact`          | 压缩对话历史             |
| `/model`            | 列出可用模型             |
| `/model <number>`   | 按编号切换模型           |
| `/model <query>`    | 按名称过滤模型           |
| `/whoami`           | 显示你的用户 ID 用于配置 |

## 配置参考

以下所有设置都通过 `anna onboard` 管理面板管理。

| 字段          | 描述                                      | 默认值    |
| ------------- | ----------------------------------------- | --------- |
| `app_id`      | QQ Bot AppID                              | （必需）  |
| `app_secret`  | QQ Bot AppSecret                          | （必需）  |
| `group_mode`  | 群组行为：`mention`、`always`、`disabled` | `mention` |
| `allowed_ids` | 允许的用户 OpenID（空 = 所有人）          | `[]`      |
