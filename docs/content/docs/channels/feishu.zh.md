---
title: Feishu Bot
---

anna 内置了通过 WebSocket 连接的 Feishu（Lark）机器人，因此不需要公网 webhook。现在 Feishu 集成只负责聊天通道：消息、流式回复、线程、群聊和通知仍然由 anna 处理；日历、文档、任务等工作区自动化已经迁移到 `lark-cli`。

## 设置

1. 在 [飞书开放平台](https://open.feishu.cn/) 创建应用。
2. 启用 **Bot** 能力。
3. 在 **事件订阅** 中添加：
   - `im.message.receive_v1`
   - 如果需要表情事件，再添加 `im.message.reaction.created_v1`
4. 复制 App ID、App Secret、Encrypt Key 和 Verification Token。
5. 运行 `anna --open`，在管理面板里配置 Feishu 频道。
6. 启动 anna：

```bash
anna
```

## Lark 工作区自动化

旧的内置 `feishu_*` 工具和 `/auth` 流程已经移除。

如果你要操作日历、任务、文档、知识库、表格、云盘、联系人等工作区数据，请按需自行安装 `lark-cli` skill，并配合外部 [`lark-cli`](https://github.com/larksuite/cli) 工具。

常见初始化流程：

```bash
command -v lark-cli
npm install -g @larksuite/cli
lark-cli config init --new
lark-cli auth login --recommend
lark-cli auth status
```

用户自行安装的 `lark-cli` skill 可以覆盖原来的 `feishu_calendar`、`feishu_task`、`feishu_im`、`feishu_doc`、`feishu_wiki`、`feishu_sheets`、`feishu_drive`、`feishu_bitable`、`feishu_user` 和 `feishu_search` 等工作流。

## 多用户支持

每个 Feishu 用户都会通过平台身份自动解析。会话按用户和 agent 隔离，因此不同用户拥有各自独立的记忆和默认 agent 状态。

## 流式回复

机器人会通过原地编辑消息来实现流式输出：

1. 先快速发送占位回复
2. 模型生成时持续更新内容
3. 最终写入完整回复和耗时信息

执行工具时的状态也会在流式消息中简要显示。

## 支持的消息类型

| 类型 | 行为 |
| --- | --- |
| 文本 | 作为普通文本发送给 LLM |
| 图片 | 下载后作为多模态输入发送 |
| 富文本 Post | 原始 JSON 直接传给 LLM |
| 音频 | 转成带时长的描述文本 |
| 视频 | 转成带时长的描述文本 |
| 文件 | 转成带文件信息的描述文本 |
| 表情贴纸 | 转成描述文本 |
| 位置 | 尽量附带坐标信息的描述文本 |
| 分享的群聊/用户 | 转成描述文本 |
| 合并转发 | 转成摘要标记 |

## 原生线程

如果用户在 Feishu 线程中发消息，anna 会在线程内回复，并把会话作用域绑定到该线程根消息。线程外消息仍然使用父聊天会话。

## 群组行为

`group_mode` 控制 anna 在群聊中的响应方式：

- `mention`：只有被 @ 时才回复
- `always`：回复所有消息
- `disabled`：从不在群里回复

你也可以通过 `groups` 字段为特定群单独覆盖配置。

## 命令

Feishu 支持标准聊天命令：

| 命令 | 说明 |
| --- | --- |
| `/new` | 开启新会话 |
| `/compact` | 压缩当前会话历史 |
| `/model` | 列出或切换模型 |
| `/agent` | 列出或切换 agent |
| `/whoami` | 显示你的平台身份 |

## 配置参考

```json
{
  "app_id": "FEISHU_APP_ID",
  "app_secret": "FEISHU_APP_SECRET",
  "encrypt_key": "",
  "verification_token": "",
  "group_mode": "mention",
  "enable_notify": false,
  "groups": {
    "oc_example": {
      "group_mode": "always",
      "system_prompt": "这个群里请作为基础设施助手回复。"
    }
  }
}
```

| 字段 | 说明 |
| --- | --- |
| `app_id` | 飞书应用 App ID |
| `app_secret` | 飞书应用 App Secret |
| `encrypt_key` | 可选的事件加密密钥 |
| `verification_token` | 可选的事件校验 token |
| `group_mode` | 默认群聊行为：`mention`、`always` 或 `disabled` |
| `enable_notify` | 允许调度器和 `notify` 输出发送到 Feishu |
| `groups` | 按 Feishu `chat_id` 配置的群级覆盖项 |
