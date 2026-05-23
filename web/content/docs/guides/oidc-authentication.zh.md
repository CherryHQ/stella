---
title: OIDC 认证
---

Stella 支持通过任何兼容 OIDC 的身份提供商登录——Zitadel、Keycloak、Authentik、Auth0 等。配置 OIDC 后，登录页面会显示"登录"按钮，点击后浏览器将跳转到身份提供商完成认证并返回。

## 快速开始

启动服务器前，设置以下环境变量：

```bash
OIDC_PROVIDER_NAME=Zitadel          # 登录按钮上显示的名称
OIDC_ISSUER_URL=https://your-idp    # 身份提供商的 OIDC discovery URL
OIDC_CLIENT_ID=your-client-id
OIDC_CLIENT_SECRET=your-client-secret
OIDC_REDIRECT_URL=https://your-stella-host/auth/callback/Zitadel
```

然后正常启动服务器：

```bash
stella server
```

登录页面将显示 **Sign in Zitadel** 按钮。点击后会启动带 PKCE 的 OIDC 授权码流程——Stella 不存储任何密码。

## 环境变量

| 变量                  | 是否必填 | 说明                                                             |
| --------------------- | -------- | ---------------------------------------------------------------- |
| `OIDC_ISSUER_URL`     | 是       | 身份提供商的 OIDC discovery URL                                  |
| `OIDC_CLIENT_ID`      | 是       | 在身份提供商注册的 Client ID                                     |
| `OIDC_CLIENT_SECRET`  | 是       | Client Secret                                                    |
| `OIDC_REDIRECT_URL`   | 是       | 回调地址：`https://your-host/auth/callback/{OIDC_PROVIDER_NAME}` |
| `OIDC_PROVIDER_NAME`  | 是       | 登录按钮上显示的名称                                             |
| `OIDC_SCOPES`         | 否       | 逗号分隔的 scope（默认：`openid,email,profile`）                 |
| `OIDC_ORG_ID_CLAIM`   | 否       | 携带组织 ID 的 JWT claim（如 `urn:zitadel:iam:org:id`）          |
| `OIDC_ORG_NAME_CLAIM` | 否       | 携带组织名称的 JWT claim                                         |

OIDC 完全可选。未设置 `OIDC_ISSUER_URL` 时，服务器正常启动并显示用户名/密码登录表单。

## 身份提供商配置

### Zitadel

1. 在 Zitadel 实例中创建一个项目。
2. 添加 **Web** 应用。选择 **Code** 流程并启用 **PKCE**。
3. 将重定向地址设置为 `https://your-stella-host/auth/callback/Zitadel`。
4. 将 **Client ID** 和 **Client Secret** 填入上述环境变量。
5. 将 `OIDC_ISSUER_URL` 设置为你的 Zitadel 域名（如 `https://your-org.zitadel.cloud`）。

如需使用 Zitadel 的组织 claim 进行多租户访问控制，还需设置：

```bash
OIDC_ORG_ID_CLAIM=urn:zitadel:iam:org:id
OIDC_ORG_NAME_CLAIM=urn:zitadel:iam:org:name
```

### 其他提供商

任何符合 OIDC 标准的提供商均可使用。唯一要求是身份提供商在 ID token 中返回 `email_verified: true`——Stella 会拒绝无法确认邮箱验证的登录。

## 账号关联机制

首次通过 OIDC 登录时，Stella 会根据 ID token 中的邮箱地址查找账号：

- **邮箱与已有账号匹配** — Stella 将 OIDC 身份关联到该账号，已有数据（智能体、对话、密钥库）完整保留。
- **未找到匹配** — Stella 为你创建新账号。

自动关联要求 Stella 中已存储的用户名为邮箱地址。如果不是，请参阅下方升级说明。

## 从已有安装升级

Stella 在首次启动时会自动将现有用户和渠道身份复制到新认证表中，无需手动迁移。

如果已有用户名不是邮箱地址，需要在启用 OIDC 前更新，以便自动关联生效。在设置 `OIDC_ISSUER_URL` 之前，在服务器上执行：

```bash
stella auth link-user --user-id <id> --email <your@email.com>
```

此命令会更新该用户存储的邮箱，使 OIDC 登录时能自动找到并关联账号。

## 安全说明

- Session 以 SHA-256 哈希形式存储——原始 token 不会离开浏览器 cookie。
- OIDC state 参数使用从 `STELLA_VAULT_KEY` 派生的密钥进行 HMAC 签名，防止 CSRF 攻击。
- 启用 OIDC 前必须先设置 `STELLA_VAULT_KEY`。
