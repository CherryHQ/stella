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

anna 现在会内置生成好的 `lark` system skill，发布构建也会自动嵌入 `lark-cli`。如果你要操作日历、任务、文档、知识库、表格、云盘、联系人等工作区数据，直接启用内置 `lark` skill，并配合 [`lark-cli`](https://github.com/larksuite/cli) 使用即可。

常见初始化流程：

```bash
command -v lark-cli || npm install -g @larksuite/cli
lark-cli config init --new
lark-cli auth login --recommend
lark-cli auth status
```

内置 `lark` skill 可以覆盖原来的 `feishu_calendar`、`feishu_task`、`feishu_im`、`feishu_doc`、`feishu_wiki`、`feishu_sheets`、`feishu_drive`、`feishu_bitable`、`feishu_user` 和 `feishu_search` 等工作流。

## 自动注册用户

为某个 Feishu 频道实例开启后，anna 会在该机器人的租户员工第一次发消息时，自动为其创建 Anna 账号，无需手动注册或执行 `/link`。

### 工作原理

1. 用户发送消息。
2. 自动注册只会在机器人实际处理这条消息时触发。在群聊里，默认 `group_mode` 是 `mention`，所以用户必须先 `@` 机器人；除非你把频道或群组覆盖配置改成 `always`。
3. Anna 确定机器人的租户键：
   - 如果显式配置了 `tenant_key`，就使用该值。
   - 否则 anna 会在启动时通过 Feishu tenant API 自动探测。
4. 如果消息事件里带有 `tenant_key`，且它与机器人租户不一致，anna 会跳过该发送者的自动注册。
5. Anna 调用飞书联系人 API（`contact.v3.user.get`）获取用户的 `union_id`、显示名称和邮箱。
6. 以邮箱本地部分作为用户名创建 Anna 账号（例如 `alice@corp.com` → `alice`），无邮箱时回退到 `feishu-<union_id[:8]>`。用户名冲突时加 `-2`、`-3` 等后缀。
7. 自动创建的用户没有密码，可以立即与机器人对话，但在管理员设置密码前无法登录管理面板。
8. 自动注册的用户角色为 `user`，默认使用系统默认 agent。

自动注册是 best-effort 的：如果租户探测失败，或联系人 API 查询失败，消息仍然会按正常通道流程继续处理，但不会创建 Anna 用户。

### 所需应用权限

在飞书开放平台的 **权限管理** 中添加以下权限：

- `contact:user.base:readonly`
- `contact:user.id:readonly`

### 如何获取 tenant_key

登录飞书管理后台，进入 **企业信息**，找到 **企业标识（Tenant Key）**。

当前实现里 `tenant_key` 不是必填项，因为 anna 可以在启动时自动探测；但仍然建议显式填写，这样可以减少一种失败路径，让自动注册行为更稳定、更可预期。

### 配置示例

```json
{
  "app_id": "FEISHU_APP_ID",
  "app_secret": "FEISHU_APP_SECRET",
  "tenant_key": "YOUR_TENANT_KEY",
  "auto_provision": true
}
```

> **注意：** 共享群中的外部访客不会被自动注册。如果他们的 tenant_key 与机器人租户不一致，anna 会跳过账号创建。这是预期行为。

> **注意：** 若系统中尚无管理员账号，自动注册会被拒绝，直到第一个管理员通过管理面板完成注册。这样可以防止全新部署陷入无管理员的困境。

## 多用户支持

每个 Feishu 用户都会通过平台身份自动解析。会话按用户和 agent 隔离，因此不同用户拥有各自独立的记忆和默认 agent 状态。

## 流式回复

机器人会通过原地编辑消息来实现流式输出：

1. 先快速发送占位回复
2. 模型生成时持续更新内容
3. 最终写入完整回复和耗时信息

执行工具时的状态也会在流式消息中简要显示。

## 支持的消息类型

| 类型            | 行为                                                  |
| --------------- | ----------------------------------------------------- |
| 文本            | 作为普通文本发送给 LLM                                |
| 图片            | 下载后作为多模态输入发送                              |
| 富文本 Post     | 原始 JSON 直接传给 LLM                                |
| 音频            | 转成带时长的描述文本                                  |
| 视频            | 转成带时长的描述文本                                  |
| 文件            | 下载并保存到磁盘，附带 kreuzberg 提取提示传递给 Agent |
| 表情贴纸        | 转成描述文本                                          |
| 位置            | 尽量附带坐标信息的描述文本                            |
| 分享的群聊/用户 | 转成描述文本                                          |
| 合并转发        | 转成摘要标记                                          |

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

| 命令       | 说明             |
| ---------- | ---------------- |
| `/new`     | 开启新会话       |
| `/compact` | 压缩当前会话历史 |
| `/model`   | 列出或切换模型   |
| `/agent`   | 列出或切换 agent |
| `/whoami`  | 显示你的平台身份 |

## 配置参考

```json
{
  "app_id": "FEISHU_APP_ID",
  "app_secret": "FEISHU_APP_SECRET",
  "encrypt_key": "",
  "verification_token": "",
  "group_mode": "mention",
  "enable_notify": false,
  "tenant_key": "",
  "auto_provision": false,
  "groups": {
    "oc_example": {
      "group_mode": "always",
      "system_prompt": "这个群里请作为基础设施助手回复。"
    }
  }
}
```

| 字段                 | 说明                                                             |
| -------------------- | ---------------------------------------------------------------- |
| `app_id`             | 飞书应用 App ID                                                  |
| `app_secret`         | 飞书应用 App Secret                                              |
| `encrypt_key`        | 可选的事件加密密钥                                               |
| `verification_token` | 可选的事件校验 token                                             |
| `group_mode`         | 默认群聊行为：`mention`、`always` 或 `disabled`                  |
| `enable_notify`      | 允许调度器和 `notify` 输出发送到 Feishu                          |
| `tenant_key`         | 企业 Tenant Key。可选：anna 可在启动时自动探测，但仍建议显式配置 |
| `auto_provision`     | 自动为这个 Feishu 频道实例实际处理到的用户创建 Anna 账号         |
| `groups`             | 按 Feishu `chat_id` 配置的群级覆盖项                             |

## 自动注册排障

如果新的 Feishu 用户没有被创建，先检查这些项目：

1. **是否在正确的 Feishu 实例上启用了该功能。** 自动注册是按频道实例配置的，不是全局开关。
2. **机器人是否真的处理到了这条消息。** 在群聊里，`group_mode: mention` 需要先 `@` 机器人。
3. **`tenant_key` 是否已配置，或启动时自动探测是否成功。** 如果租户探测失败且没有显式配置 `tenant_key`，自动注册会被跳过。
4. **Feishu 应用是否具备以下权限：**
   - `contact:user.base:readonly`
   - `contact:user.id:readonly`
5. **Anna 中是否已经至少存在一个管理员。** 全新部署在第一个管理员创建前会拒绝自动注册。
6. **发送者是否是内部租户成员。** 外部访客本来就不会被自动注册。
7. **修改配置后是否重启了 anna。**

一个更稳妥的配置示例是：

```json
{
  "app_id": "FEISHU_APP_ID",
  "app_secret": "FEISHU_APP_SECRET",
  "tenant_key": "YOUR_TENANT_KEY",
  "auto_provision": true,
  "group_mode": "always"
}
```

只有在你希望群里每条消息都触发自动注册和回复时，才使用 `group_mode: "always"`。否则保持 `mention`，并确保用户第一次联系时先 `@` 机器人。
