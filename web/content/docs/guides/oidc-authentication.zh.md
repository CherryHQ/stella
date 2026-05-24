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

## 本地 OIDC 发行方

Stella 可以作为自己的 OIDC 身份提供商。适用于单用户或本地部署场景——例如将 Stella 的用户名/密码凭证用于其他应用的 OIDC 登录。

**这不是生产环境外部身份提供商的替代方案。** 仅适用于本地或自包含部署。

### 启用本地发行方

生成 P-256 签名密钥：

```bash
openssl ecparam -name prime256v1 -genkey -noout -out local-oidc.key
openssl pkcs8 -topk8 -nocrypt -in local-oidc.key -out local-oidc-pkcs8.pem
```

然后设置：

```bash
LOCAL_OIDC_ENABLED=true
LOCAL_OIDC_ISSUER_URL=https://your-stella-host/oidc/local
LOCAL_OIDC_CLIENT_ID=stella-local
LOCAL_OIDC_REDIRECT_URIS=https://your-app/callback
LOCAL_OIDC_SIGNING_KEY="$(cat local-oidc-pkcs8.pem)"
# 可选：
LOCAL_OIDC_CLIENT_SECRET=         # 留空表示使用公开客户端（必须使用 PKCE）
LOCAL_OIDC_KEY_ID=local-1
```

发行方在 `/oidc/local` 下暴露标准 OIDC 端点：

| 端点      | 路径                                           |
| --------- | ---------------------------------------------- |
| Discovery | `/oidc/local/.well-known/openid-configuration` |
| JWKS      | `/oidc/local/jwks`                             |
| 授权      | `/oidc/local/authorize`                        |
| Token     | `/oidc/local/token`                            |
| Userinfo  | `/oidc/local/userinfo`                         |

### 环境变量

| 变量                       | 是否必填          | 说明                                            |
| -------------------------- | ----------------- | ----------------------------------------------- |
| `LOCAL_OIDC_ENABLED`       | 是（值为 `true`） | 必须恰好为 `true` 才会启用                      |
| `LOCAL_OIDC_ISSUER_URL`    | 是                | 主机上 `/oidc/local` 的完整 URL                 |
| `LOCAL_OIDC_CLIENT_ID`     | 是                | 依赖方的 Client ID                              |
| `LOCAL_OIDC_REDIRECT_URIS` | 是                | 逗号分隔的精确匹配重定向 URI                    |
| `LOCAL_OIDC_SIGNING_KEY`   | 是                | PEM 格式的 ECDSA P-256 私钥（或 PEM 的 base64） |
| `LOCAL_OIDC_CLIENT_SECRET` | 否                | Client Secret；公开 PKCE 客户端请留空           |
| `LOCAL_OIDC_KEY_ID`        | 否                | JWKS 中的 Key ID（默认：`local-1`）             |

### 安全限制

- 仅支持**授权码 + PKCE** 流程。
- 重定向 URI 精确匹配——不支持通配符。
- 签名密钥在启动时加载；密钥轮换需要重启服务器。
- 不支持动态客户端注册，客户端配置为静态环境变量配置。
- 未使用 TLS 时请勿将本地发行方暴露到公网。

## 安全说明

- Session 以 SHA-256 哈希形式存储——原始 token 不会离开浏览器 cookie。
- OIDC state 参数使用从 `STELLA_VAULT_KEY` 派生的密钥进行 HMAC 签名，防止 CSRF 攻击。
- 启用 OIDC 前必须先设置 `STELLA_VAULT_KEY`。
