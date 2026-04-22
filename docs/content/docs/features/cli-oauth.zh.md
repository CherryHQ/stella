---
title: CLI OAuth 认证
---

## 概述

CLI OAuth 功能允许 Agent 在沙盒会话中直接使用 `gh`（GitHub CLI）和 `lark-cli`，无需手动认证。Anna 在宿主机上完成 OAuth 设备流程，将版本化的令牌包存储到个人密钥库中，并在每次沙盒会话启动时自动注入最新的运行时令牌。

## 前提条件

用户连接前，管理员须在对应的认证插件中配置好应用凭据：

- **GitHub**：管理员需在 GitHub 认证插件设置中填入 OAuth 应用的 Client ID 和 Client Secret。
- **Lark / 飞书**：管理员需在 Lark 认证插件设置中填入 App ID、App Secret 以及品牌标识（`lark` 或 `feishu`）。

## 连接步骤

1. 打开 Anna 管理面板，进入**个人资料**页面。
2. 找到 **OAuth CLI 凭据**区域。
3. 点击要连接的服务商旁边的**连接**按钮。
4. Anna 启动设备流程，并显示验证 URL 和用户码。
5. 在浏览器中打开该 URL，输入用户码并完成授权。
6. Anna 轮询授权结果。授权成功后，令牌包将保存至您的密钥库。

随时可点击服务商旁的**断开连接**取消绑定。

## 使用 CLI 工具

连接成功后，Agent 在沙盒会话中可以直接运行 `gh` 和 `lark-cli` 命令，无需任何额外配置。Anna 会将包装脚本目录添加到 `PATH` 的最前面，使每次调用都自动获得正确的认证凭据。对于 `lark-cli`，Anna 会注入 `LARKSUITE_CLI_USER_ACCESS_TOKEN`、`LARKSUITE_CLI_APP_ID` 和 `LARKSUITE_CLI_BRAND`，因此不需要在每个会话里再执行 `config init`。

示例（由 Agent 在 bash 工具调用中执行）：

```sh
gh issue list --repo owner/repo
lark-cli message send --chat-id <id> --text "Hello"
```

## 已知限制

### Lark 令牌有效期

Lark 用户访问令牌有效期约为 **2 小时**。Anna 仅在会话启动时刷新令牌。若 Agent 会话时长超过令牌有效期，`lark-cli` 调用将因认证失败而报错。重新开启一个 Anna 会话，即可自动获取刷新后的令牌。

### 重启会丢失进行中的设备流程

未完成授权的设备流程状态保存在内存中。Anna 进程重启后，这些状态将丢失。如果您正在浏览器中完成授权时 Anna 发生重启，需要在个人资料页面重新发起授权流程。

## 安全模型

OAuth 令牌包（`GH_OAUTH`、`LARK_CLI_OAUTH`）使用与其他密钥库条目相同的 age 加密方式加密存储。它们仅在宿主机上使用：原始 JSON 包不会传入沙盒进程的环境变量。沙盒进程只能获取派生的运行时令牌（例如 GitHub 的 `GH_TOKEN`），无法访问刷新凭据或 OAuth 应用密钥。
