---
name: lark-shared
version: 1.0.0
description: "飞书/Lark CLI 共享基础（Stella 适配版）：说明 Stella 会话中的 `/bin` + 环境变量认证模型、`--as user` / `--as bot` 选择、scope / Permission denied 处理，以及何时才需要回退到上游 `config init` / `auth login` 流程。"
---

# lark-cli 共享规则

本技能指导你如何通过lark-cli操作飞书资源, 以及有哪些注意事项。

## Stella 会话中的运行方式（优先遵循本节）

在 Stella 的沙盒会话里，
`lark-cli` 走的是 **`$STELLA_HOME/bin` + 环境变量注入** 模式，不是上游文档默认假设的“先 `config init`，再 `auth login`”本地配置模式。

- 直接运行 `lark-cli ...` 即可。Stella 会把 `$STELLA_HOME/bin` 放到会话 `PATH` 前面，让 `lark-cli` 直接解析到 Stella 管理的二进制。
- Stella 会在会话启动时注入 `LARKSUITE_CLI_USER_ACCESS_TOKEN`、`LARKSUITE_CLI_APP_ID`、`LARKSUITE_CLI_BRAND`。因此 **在 Stella 会话里默认不要先执行 `lark-cli config init` 或 `lark-cli auth login`**。
- 把后续文档里所有“先 `auth login --domain` / `auth login --scope`”的要求，映射为：**用 `oauth` 工具确认 Lark 已连接（oauth status 指令），所需 scope 已在 Lark 应用侧开通，然后开启一个新的 Stella 会话重试**。
- 如果用户只是想在自己机器上单独配置一个**脱离 Stella** 的 `lark-cli`，那才回退到上游原生的 `config init` / `auth login` 流程。

## Stella 中的身份选择

| 身份          | 在 Stella 中如何获得                                                                                                   | 适用场景                                         |
| ------------- | ---------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------ |
| user 用户身份 | Stella 注入运行时用户令牌；优先使用 `--as user`（或命令默认身份）                                                      | 访问用户自己的日历、云文档、任务、邮箱等个人资源 |
| bot 应用身份  | 需要用户在 Stella 之外显式准备 app 配置 / tenant token；Stella 当前**不自动注入** bot 所需的 app secret 或 config 文件 | 仅在用户明确说明已完成这套手动配置时使用         |

### 身份选择原则

- **默认优先 user**：大多数日历、云空间、文档、任务、邮箱等工作区请求，本质上都是“代表当前用户操作自己的资源”。
- **不要擅自假设 bot 可用**：如果文档或任务要求 `--as bot`、`tenant_access_token`、appSecret 或本地 config 文件，而当前会话只有 Stella 注入的 user 运行时环境，就应先停下来说明这是**Stella 当前未自动接线**的手动配置路径。
- **Bot 看不到用户私有资源**：即便用户自己在 Stella 外部完成了 bot 配置，`--as bot` 也仍然看不到用户的私有日历、个人云文档、邮箱等资源。

## Stella 中的认证与提权处理

### 未连接、过期或认证失败

- 如果 `lark-cli` 提示未登录、缺少 access token、401/expired，先用 `oauth` 工具检查 Lark 连接状态（oauth status 指令）；如需重新连接，执行 oauth connect（provider=feishu），或引导用户前往 Credentials → OAuth CLI Credentials 重新授权。
- Lark user access token 约 2 小时过期；Stella 只在**会话启动时**刷新。已连接但中途过期时，直接开启一个新的 Stella 会话。
- 重新开启会话后仍失败，说明 refresh token 也可能失效或授权被撤销；此时应让用户从 Credentials → OAuth CLI Credentials 断开并重新连接 Lark，而不是在会话里继续尝试 `auth login`。

### 权限不足 / scope 不足

- 先查看错误里的 `permission_violations`、`console_url`、`hint`。
- 如果缺的是**应用 scope**，把 `console_url` 提供给用户或管理员，让他们去 Lark 开发者后台开通对应权限。
- 应用 scope 开通后，在 Stella 会话里**不要**继续执行 `lark-cli auth login --scope ...`；应让用户按需重新连接 Lark，并开启一个新的 Stella 会话后再重试。
- 只有当用户明确要求“配置一套独立于 Stella 的本地 `lark-cli` 环境”时，才执行上游文档里的 `config init` / `auth login` 指令。

## 更新检查

lark-cli 命令执行后，如果检测到新版本，JSON 输出中会包含 `_notice.update` 字段（含 `message`、`command` 等）。

**当你在输出中看到 `_notice.update` 时，完成用户当前请求后，主动提议帮用户更新**：

1. 告知用户当前版本和最新版本号
2. 提议执行更新（CLI 和 Skills 需要同时更新）：
   ```bash
   npm update -g @larksuite/cli && npx skills add larksuite/cli -g -y
   ```
3. 更新完成后提醒用户：**退出并重新打开 AI Agent**以加载最新 Skills

**规则**：不要静默忽略更新提示。即使当前任务与更新无关，也应在完成用户请求后补充告知。

## 安全规则

- **禁止输出密钥**（appSecret、accessToken）到终端明文。
- **写入/删除操作前必须确认用户意图**。
- 用 `--dry-run` 预览危险请求。
