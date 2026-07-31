---
title: OIDC 认证
---

Stella 支持三种登录方式：

1. **本地密码登录**：Stella 保存 bcrypt 密码哈希，并直接创建 Stella 会话。
2. **外部 OIDC**：接入一个标准 OpenID Connect 身份提供商，例如 Zitadel、Keycloak、Authentik、Auth0、Okta 或 Azure AD。
3. **OAuth 登录提供商**：同一个 Stella 实例可以显示多个登录按钮，例如飞书、Google、GitHub 或自定义 OAuth 提供商。

配置外部登录后，登录页会显示对应的登录按钮，点击后浏览器会跳转到提供商完成认证并返回。

## 本地密码登录

未配置外部 OIDC 提供商（未设置 `OIDC_ISSUER_URL`）时，Stella 会启用本地邮箱密码登录。本地登录不再暴露 OIDC issuer 端点；密码提交通过 Stella 的 JSON API 完成，并直接创建 Stella 会话。

第一个注册的用户自动成为管理员。创建 bootstrap 管理员后，本地自助注册默认关闭。只有当你希望后续用户也能自行创建本地账号时，才设置 `LOCAL_PASSWORD_ALLOW_REGISTRATION=true`。

启用自助注册时，如需减少误注册，设置 `LOCAL_PASSWORD_ALLOWED_EMAIL_DOMAINS` 为逗号分隔的允许邮箱域名列表。只有这些域名（及其子域名）下的邮箱才能注册；不设置则允许任意邮箱。它**不会**验证邮箱所有权——真正的安全边界请使用外部 OIDC/OAuth 提供商。

```bash
LOCAL_PASSWORD_ALLOW_REGISTRATION=true
LOCAL_PASSWORD_ALLOWED_EMAIL_DOMAINS=cicc.com.cn,example.com
```

旧的 `LOCAL_OIDC_ALLOW_REGISTRATION` 和 `LOCAL_OIDC_ALLOWED_EMAIL_DOMAINS` 仍会兼容读取，但新部署应使用 `LOCAL_PASSWORD_*` 名称。

### 安全限制

- 本地密码登录只受密码保护。
- `LOCAL_PASSWORD_ALLOWED_EMAIL_DOMAINS` 只在注册时检查用户提交的邮箱字符串，不是邮箱验证，也不影响已有用户登录。
- 生产访问控制优先使用飞书 tenant allowlist、Google/GitHub verified email allowlist，或外部 OIDC 提供商。
- 如果 Stella 位于反向代理后，并且你希望登录限流使用真实客户端 IP，请将 `STELLA_TRUSTED_PROXIES` 设置为代理 IP 或 CIDR 网段。未设置时，Stella 会在认证限流中忽略 `X-Forwarded-For` 和 `X-Real-IP`。

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
stellad server
```

登录页面将显示 **Sign in Zitadel** 按钮。点击后会启动带 PKCE 的 OIDC 授权码流程——Stella 不存储任何密码。

### 环境变量

| 变量                 | 是否必填 | 说明                                                             |
| -------------------- | -------- | ---------------------------------------------------------------- |
| `OIDC_PROVIDER_NAME` | 是       | 登录按钮上显示的名称                                             |
| `OIDC_ISSUER_URL`    | 是       | 身份提供商的 OIDC discovery URL                                  |
| `OIDC_CLIENT_ID`     | 是       | 在身份提供商注册的 Client ID                                     |
| `OIDC_REDIRECT_URL`  | 是       | 回调地址：`https://your-host/auth/callback/{OIDC_PROVIDER_NAME}` |
| `OIDC_CLIENT_SECRET` | 否       | Client Secret；公开客户端（仅 PKCE）请留空                       |
| `OIDC_SCOPES`        | 否       | 逗号分隔的 scope（默认：`openid,email,profile`）                 |

设置 `OIDC_ISSUER_URL` 后，登录页会用外部 OIDC 提供商替换本地密码登录。已配置的 OAuth provider 仍会同时显示。

## OAuth 登录提供商

当提供商不是标准 OIDC issuer，或你希望同一个 Stella 实例显示多个登录按钮时，可以使用 OAuth 登录。Stella 内置了 `github`、`google`、`feishu` 预设；自定义提供商可以通过授权、token 和 userinfo URL 接入。

通过逗号分隔列表启用提供商：

```bash
AUTH_OAUTH_PROVIDERS=google,github,feishu
```

每个提供商使用大写 provider ID 作为环境变量前缀：

```bash
AUTH_OAUTH_GOOGLE_CLIENT_ID=your-google-client-id
AUTH_OAUTH_GOOGLE_CLIENT_SECRET=your-google-client-secret
AUTH_OAUTH_GOOGLE_ALLOWED_EMAIL_DOMAINS=example.com

AUTH_OAUTH_GITHUB_CLIENT_ID=your-github-client-id
AUTH_OAUTH_GITHUB_CLIENT_SECRET=your-github-client-secret
AUTH_OAUTH_GITHUB_ALLOWED_EMAIL_DOMAINS=example.com

AUTH_OAUTH_FEISHU_CLIENT_ID=cli_xxx
AUTH_OAUTH_FEISHU_CLIENT_SECRET=your-feishu-app-secret
AUTH_OAUTH_FEISHU_ALLOWED_TENANT_KEYS=tenant_key_from_feishu
```

如果设置了 `STELLA_BASE_URL`，Stella 会自动使用 `https://your-host/auth/callback/{provider}` 作为回调地址。也可以用 `AUTH_OAUTH_{PROVIDER}_REDIRECT_URL` 单独覆盖。

### OAuth 环境变量

| 变量                                           | 是否必填 | 说明                                                             |
| ---------------------------------------------- | -------- | ---------------------------------------------------------------- |
| `AUTH_OAUTH_PROVIDERS`                         | 是       | 逗号分隔的 provider ID，例如 `google,github,feishu`              |
| `AUTH_OAUTH_{PROVIDER}_CLIENT_ID`              | 是       | OAuth client ID / app ID                                         |
| `AUTH_OAUTH_{PROVIDER}_CLIENT_SECRET`          | 是       | OAuth client secret / app secret                                 |
| `AUTH_OAUTH_{PROVIDER}_REDIRECT_URL`           | 否       | 回调地址；默认使用 `STELLA_BASE_URL/auth/callback/{provider}`    |
| `AUTH_OAUTH_{PROVIDER}_SCOPES`                 | 否       | 空格或逗号分隔的 scope；内置提供商已有安全默认值                 |
| `AUTH_OAUTH_{PROVIDER}_ALLOWED_EMAIL_DOMAINS`  | 是\*     | 允许登录的已验证邮箱域名；支持精确域名和子域名                   |
| `AUTH_OAUTH_{PROVIDER}_ALLOWED_TENANT_KEYS`    | 是\*     | 允许登录的租户 key；飞书登录必填                                 |
| `AUTH_OAUTH_{PROVIDER}_REQUIRE_EMAIL_VERIFIED` | 否       | 针对 generic OAuth provider，要求 `email_verified`；默认：`true` |

每个 OAuth provider 必须设置 `ALLOWED_EMAIL_DOMAINS` 或该 provider 明确支持的 tenant allowlist。Google 和 GitHub 使用 `ALLOWED_EMAIL_DOMAINS`；飞书必须设置 `ALLOWED_TENANT_KEYS`。这样可以避免误把 Stella 开放给该 provider 下的所有账号。

### 飞书登录

在飞书开放平台创建网页应用，并在**安全设置**中添加回调地址：

```text
https://your-stella-host/auth/callback/feishu
```

推荐配置：

```bash
STELLA_BASE_URL=https://your-stella-host
AUTH_OAUTH_PROVIDERS=feishu
AUTH_OAUTH_FEISHU_CLIENT_ID=cli_xxx
AUTH_OAUTH_FEISHU_CLIENT_SECRET=your-feishu-app-secret
AUTH_OAUTH_FEISHU_ALLOWED_TENANT_KEYS=your_tenant_key
```

Stella 默认请求 `contact:user.email:readonly`，用于从飞书获取用户邮箱。飞书用户邮箱字段是通讯录数据，不等同于实时邮箱验证，因此必须设置 `AUTH_OAUTH_FEISHU_ALLOWED_TENANT_KEYS`。如果飞书没有返回邮箱，Stella 会用类似 `union_id@tenant_key.feishu.local` 的稳定内部邮箱创建账号。邮箱域名 allowlist 适合作为额外过滤；一旦启用，飞书就必须返回匹配的邮箱。

#### 飞书工作区工具

登录只申请身份权限范围（`contact:user.email:readonly`），所以登录 URL 很短、认证只需一次快速授权确认。它**不会**授予飞书工具的访问权限。

工作区工具的认证与登录相互独立，并按各自集成方式完成认证。详见 [OAuth 连接](./oauth-connections)。

### 自定义 OAuth 提供商

非预设提供商需要显式提供端点：

```bash
AUTH_OAUTH_PROVIDERS=acme
AUTH_OAUTH_ACME_CLIENT_ID=client-id
AUTH_OAUTH_ACME_CLIENT_SECRET=client-secret
AUTH_OAUTH_ACME_AUTH_URL=https://idp.example/oauth/authorize
AUTH_OAUTH_ACME_TOKEN_URL=https://idp.example/oauth/token
AUTH_OAUTH_ACME_USERINFO_URL=https://idp.example/oauth/userinfo
AUTH_OAUTH_ACME_ALLOWED_EMAIL_DOMAINS=example.com
```

自定义 OAuth provider 默认必须从 userinfo 端点返回 `email_verified: true`。如果你信任该 provider 但它不返回这个 claim，可以设置 `AUTH_OAUTH_{PROVIDER}_REQUIRE_EMAIL_VERIFIED=false`；Stella 仍会要求邮箱域名 allowlist。

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

通过 OIDC 或 OAuth 登录时，Stella 会根据提供商返回的 provider 和 subject 标识关联账号。Stella 不会因为邮箱地址相同，就把一个新的外部身份静默绑定到已有账号。

- **Provider subject 已绑定** — Stella 登录到该账号，已有数据（智能体、对话、密钥库）完整保留。
- **Provider subject 未绑定** — Stella 为你创建新账号。第一个注册的用户自动获得管理员角色，后续用户获得普通用户角色。

管理员可以在 Web UI 的**设置 > 用户**页面管理用户、角色和显式登录身份绑定。

## 从已有安装升级

Stella 在首次启动时会自动将现有用户和渠道身份复制到新认证表中，无需手动迁移。

如需把外部登录绑定到已有账号，请在**设置 > 用户**页面显式创建登录身份绑定。仅邮箱匹配不会自动关联账号。

## 安全说明

- Session 以 SHA-256 哈希形式存储——原始 token 不会离开浏览器 cookie。
- OIDC state 参数使用从 `STELLA_VAULT_KEY` 派生的密钥进行 HMAC 签名，防止 CSRF 攻击。
- 启用 OIDC 前必须先设置 `STELLA_VAULT_KEY`。
