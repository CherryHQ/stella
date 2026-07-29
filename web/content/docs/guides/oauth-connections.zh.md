---
title: OAuth 连接
---

Stella OAuth 连接会向显式声明 `oauth_provider` 的工具提供用户 token。内置 `gh` 使用这条链路；飞书和 Lark OAuth provider 仍可供其他 manifest 工具使用，但内置 `lark-cli` 已不再消费它们。

## OAuth 连接的作用

连接服务后，Stella 会安全保存访问令牌，并只注入声明了该 provider 的已启用工具。这意味着 Stella 可以：

- 使用 `gh` 创建 GitHub issue、开 pull request、查询仓库
- 为显式声明飞书/Lark OAuth provider 的自定义 manifest 工具授权

provider 的 scope 设置和 token 刷新只影响这些消费者，不会配置或授权 lark-cli。

## 连接 GitHub

GitHub 开箱即用——不需要管理员设置。

### 方式一：在聊天中让 Stella 操作

1. 告诉 Stella："连接我的 GitHub 账号。"
2. Stella 会启动设备授权流程，给你一个 URL 和一次性验证码。
3. 在浏览器中打开 URL，输入验证码并授权。
4. Stella 检测到授权后确认连接成功。

### 方式二：使用Web UI

1. 打开Web UI，进入**凭据**页面。
2. 找到 **OAuth CLI Credentials**，点击 GitHub 旁边的**连接**。
3. Stella 会显示一个 URL 和一次性验证码。
4. 在浏览器中打开 URL，输入验证码并授权。
5. 授权完成后，状态会更新为已连接。

你可以随时在凭据页面点击**断开**来取消连接。

## 飞书 / Lark OAuth provider

飞书和 Lark provider 需要管理员先配置应用凭据，用户才能连接。如果其他 manifest 工具仍使用它们，就应保留这两张 provider 卡片。

使用飞书登录 Stella 只完成 Stella 身份认证。另行连接飞书/Lark OAuth provider 时，也只会授权 provider 卡片中“依赖此凭据”列出的工具。

如果没有工具依赖该 provider，员工无需为了 lark-cli 连接它。

### 管理员设置

在 Web UI 中配置 provider：

- 飞书/Lark 应用的 **App ID** 和 **App Secret**
- **Brand** -- 国内飞书选择 `feishu`，国际版选择 `lark`

### 员工连接

步骤与 GitHub 相同——在聊天中让 Stella 操作，或使用Web UI的凭据页面。Stella 会引导你完成同样的设备授权流程。

这是 Stella 通用 OAuth 流程，不能用于修复 lark-cli 授权。

## 管理员：管理 provider

管理员在**凭据**页面各 provider 的详情面板中管理 OAuth provider。

### 应用凭据

设置 provider 的 **Client ID** 和 **Client Secret**（飞书/Lark 为 App ID / App Secret）。保存新凭据后，已连接的用户会被标记为需要重新连接，因为旧应用签发的令牌不再匹配。

### 权限范围（Scopes）

每个 provider 都自带一份内置默认权限范围列表。管理员可以用权限范围编辑器覆盖它：

- 清单始终显示所有内置权限。没有覆盖配置时默认全部勾选；保存后则按已保存状态勾选。取消勾选后，下次授权将不再请求该权限。
- 权限范围按命名空间前缀分组、默认收起，并支持搜索。
- **恢复默认**会选中全部内置权限，并从草稿中移除自定义权限。
- 使用清单下方的输入框添加内置列表中没有的权限。粘贴内容会按换行、逗号或空格拆分并自动去重。

保存后使用当前勾选的权限。扩大请求的权限范围**不会**改变已签发的令牌：已连接用户必须重新连接才能授予新增的权限范围。

### 重新连接语义

一个连接可能显示为**已连接**但仍需要操作。出现下列情况之一时，provider 会显示**需要重新连接**状态：

- 用户连接之后应用凭据被轮换过，或
- 请求的权限范围现在包含存储令牌未持有的项（面板会列出具体缺失的权限范围）。

用户在同一面板中重新连接；令牌健康信息区块显示访问令牌与刷新令牌的过期时间，方便判断何时需要刷新。

## 使用已连接的服务

连接后，声明该 OAuth provider 的工具会在 Agent 会话中获得凭据。例如：

- "列出我仓库中的 open issue"
- "用这些更改创建一个 pull request"

Stella 会在后台自动处理认证。

## lark-cli 授权有何不同

内置 lark-cli 使用以下链路：

1. 管理员配置启用的飞书/Lark Channel，并绑定到目标 Agent。
2. Stella 在每个“员工 × Agent”工作区中初始化该 Channel 应用。
3. 员工需要用户身份或新增 scope 时，Agent 运行 `lark-cli auth status` 并发起 lark-cli 原生设备授权。
4. lark-cli 在该隔离工作区保存并刷新员工 token。

不要通过 OAuth 凭据页面或 `/auth feishu` 执行这条流程。应用 scope 在 Channel 应用对应的飞书/Lark 开发者后台管理，只开通已部署工作流实际需要的 scope。

## 故障排除

### 飞书/Lark OAuth provider token 过期

如果自定义工具使用通用飞书/Lark OAuth provider，Stella 会尽量刷新其 token；刷新失败时从凭据页面重连该 provider。这不会影响 lark-cli 的原生 token。

### 授权被重启中断

如果 Stella 在通用 provider 授权期间重启，请重新发起该连接。对于 lark-cli，只有当前 device code 已过期或明确失败后，才让 Agent 发起一次新的原生设备授权。

### GitHub 命令不工作

在Web UI的凭据页面确认你的 GitHub 账号已连接。如果状态显示未连接，重新连接即可。GitHub 令牌不会过期，所以一旦连接就可以一直使用。
