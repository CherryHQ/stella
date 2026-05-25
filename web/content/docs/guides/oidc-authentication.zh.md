---
title: OIDC 认证
---

Stella 支持通过任何兼容 OIDC 的身份提供商登录——Zitadel、Keycloak、Authentik、Auth0 等。配置 OIDC 后，登录页面会显示"登录"按钮，点击后浏览器将跳转到身份提供商完成认证并返回。

Stella 还内置了一个**本地 OIDC 发行方**，在未配置外部提供商时默认启用。无需任何额外配置即可使用本地账号登录。

## 内置本地 OIDC 发行方

未配置外部 OIDC 提供商（未设置 `OIDC_ISSUER_URL`）时，Stella 会自动启用内置的本地 OIDC 发行方。无需设置任何环境变量——签名密钥会自动生成并存储在数据目录中。

登录页面会显示**登录**按钮。你也可以点击**注册**来创建新账号。

第一个注册的用户自动成为管理员。

本地发行方在 `/oidc/local` 下暴露标准 OIDC 端点：

| 端点      | 路径                                           |
| --------- | ---------------------------------------------- |
| Discovery | `/oidc/local/.well-known/openid-configuration` |
| JWKS      | `/oidc/local/jwks`                             |
| 授权      | `/oidc/local/authorize`                        |
| Token     | `/oidc/local/token`                            |
| Userinfo  | `/oidc/local/userinfo`                         |

### 安全限制

- 仅支持**授权码 + PKCE** 流程。
- 重定向 URI 精确匹配——不支持通配符。
- 签名密钥在启动时加载；密钥轮换需要重启服务器。
- 不支持动态客户端注册，客户端配置为静态配置。
- 未使用 TLS 时请勿将本地发行方暴露到公网。

## 外部 OIDC 提供商

### 快速开始

启动服务器前，设置以下环境变量：

```bash
OIDC_PROVIDER_NAME=Zitadel          # 登录按钮上显示的名称
OIDC_ISSUER_URL=https://your-idp    # 身份提供商的 OIDC discovery URL
OIDC_CLIENT_ID=your-client-id
OIDC_REDIRECT_URL=https://your-stella-host/auth/callback/Zitadel
```

然后正常启动服务器：

```bash
stella server
```

登录页面将显示 **Sign in Zitadel** 按钮。点击后会启动带 PKCE 的 OIDC 授权码流程——Stella 不存储任何密码。

### 环境变量

| 变量                  | 是否必填 | 说明                                                             |
| --------------------- | -------- | ---------------------------------------------------------------- |
| `OIDC_PROVIDER_NAME`  | 是       | 登录按钮上显示的名称                                             |
| `OIDC_ISSUER_URL`     | 是       | 身份提供商的 OIDC discovery URL                                  |
| `OIDC_CLIENT_ID`      | 是       | 在身份提供商注册的 Client ID                                     |
| `OIDC_REDIRECT_URL`   | 是       | 回调地址：`https://your-host/auth/callback/{OIDC_PROVIDER_NAME}` |
| `OIDC_CLIENT_SECRET`  | 否       | Client Secret；公开客户端（仅 PKCE）请留空                       |
| `OIDC_SCOPES`         | 否       | 逗号分隔的 scope（默认：`openid,email,profile`）                 |
| `OIDC_ORG_ID_CLAIM`   | 否       | 携带组织 ID 的 JWT claim（如 `urn:zitadel:iam:org:id`）          |
| `OIDC_ORG_NAME_CLAIM` | 否       | 携带组织名称的 JWT claim                                         |

设置 `OIDC_ISSUER_URL` 后，外部提供商将替换登录页面上的内置本地发行方。

## 身份提供商配置

### Zitadel

#### 1. 创建项目和应用

1. 在 Zitadel 控制台中，进入**项目**并创建一个新项目（或使用已有项目）。
2. 在项目内，点击**新建**添加应用。
3. 选择 **Web** 作为应用类型。
4. 认证方式选择 **Code**。如果不需要 Client Secret，可以选择 **PKCE**。
5. 将重定向地址设置为 `https://your-stella-host/auth/callback/Zitadel`。
6. 将 **Client ID**（如果选择了 Code 流程还有 **Client Secret**）填入环境变量。
7. 将 `OIDC_ISSUER_URL` 设置为你的 Zitadel 实例 URL（如 `https://your-org.zitadel.cloud`）。

#### 2. 配置 Token 设置

默认情况下，Zitadel 不会在 ID Token 中包含用户资料 claim（如 `email`）。Stella 需要 `email` claim 来识别用户。

在应用设置中，进入 **Token** 选项卡并启用：

- **User Info inside ID Token** —— 这会将 `email`、`email_verified`、`name` 和 `picture` claim 直接添加到 ID Token 中。

未启用此设置时，登录会失败并报"email claim missing"错误。

#### 3. 启用用户注册（可选）

如果希望新用户能通过 Zitadel 的登录页面注册：

1. 进入 Zitadel 实例**设置** > **登录行为与安全**。
2. 启用**自助注册**。

启用后，Zitadel 会在登录页面显示"注册"链接。通过 Zitadel 注册的用户在首次登录时会自动在 Stella 中创建账号。

#### 4. 配置 Scope（可选）

默认 scope（`openid,email,profile`）适用于大多数场景。如果需要 Zitadel 将应用识别为 audience（某些 API 集成需要），请添加 Zitadel 项目 audience scope：

```bash
OIDC_SCOPES=openid,email,profile,urn:zitadel:iam:org:project:id:zitadel:aud
```

#### 5. 组织 Claim（可选）

如需使用 Zitadel 的组织 claim 进行多租户访问控制：

```bash
OIDC_ORG_ID_CLAIM=urn:zitadel:iam:org:id
OIDC_ORG_NAME_CLAIM=urn:zitadel:iam:org:name
```

#### 配置示例

```bash
OIDC_PROVIDER_NAME=Zitadel
OIDC_ISSUER_URL=https://your-org.zitadel.cloud
OIDC_CLIENT_ID=123456789012345678@my-project
OIDC_REDIRECT_URL=https://your-stella-host/auth/callback/Zitadel
OIDC_SCOPES=openid,email,profile,urn:zitadel:iam:org:project:id:zitadel:aud
```

### 其他提供商

任何符合 OIDC 标准的提供商均可使用。唯一要求是身份提供商在 ID Token 中返回 `email` 和 `email_verified: true`——Stella 会拒绝无法确认邮箱验证的登录。

## 账号关联机制

首次通过 OIDC 登录时，Stella 会根据 ID Token 中的邮箱地址查找账号：

- **邮箱与已有账号匹配** — Stella 将 OIDC 身份关联到该账号，已有数据（智能体、对话、密钥库）完整保留。
- **未找到匹配** — Stella 为你创建新账号。

组织中的第一个用户自动获得管理员角色。

## 从已有安装升级

Stella 在首次启动时会自动将现有用户和渠道身份复制到新认证表中，无需手动迁移。

如果已有用户名不是邮箱地址，需要在启用 OIDC 前更新，以便自动关联生效：

```bash
stella auth link-user --user-id <id> --email <your@email.com>
```

此命令会更新该用户存储的邮箱，使 OIDC 登录时能自动找到并关联账号。

## 安全说明

- Session 以 SHA-256 哈希形式存储——原始 token 不会离开浏览器 cookie。
- OIDC state 参数使用从 `STELLA_VAULT_KEY` 派生的密钥进行 HMAC 签名，防止 CSRF 攻击。
- 启用 OIDC 前必须先设置 `STELLA_VAULT_KEY`。
