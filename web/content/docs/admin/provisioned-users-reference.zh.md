---
title: 受管用户 API 参考
description: 企业开通的端点、请求字段、响应和生命周期限制。
---

以下所有端点都需要 `Authorization: Bearer stella_prv_…`。只有交互式管理员 session 可以在 `/api/admin/provisioning-tokens` 创建、列出或撤销这些开通令牌。

| 方法   | 端点                                             | 响应                                           |
| ------ | ------------------------------------------------ | ---------------------------------------------- |
| `POST` | `/api/provisioned-users`                         | `201` 用户元数据和一次性 `token`               |
| `GET`  | `/api/provisioned-users`                         | `200` `provisioned_users` 和 `next_page_token` |
| `GET`  | `/api/provisioned-users/{id}`                    | `200` 安全的用户与活跃令牌元数据               |
| `POST` | `/api/provisioned-users/{id}/channel-identities` | `201` 已创建的消息平台身份                     |
| `POST` | `/api/provisioned-users/{id}/rotate-token`       | `200` 更新后的元数据和一次性替换 `token`       |
| `POST` | `/api/provisioned-users/{id}/deactivate`         | `200` 已停用的用户元数据                       |

`id` 是 Stella 的规范 UUID。`external_id` 是唯一、不可变的字段，不是路径标识。列表请求使用 `page_size`（默认 20，最大 500）和不透明的 `page_token`。

创建需要 `external_id`、`email` 和 `name`；`token_name` 默认是 `enterprise-integration`。创建和轮换的 `expires_at` 默认 90 天，超过 365 天会被拒绝。不支持 `never_expires`。

响应只公开令牌 ID、名称、后四位、过期时间、最后使用时间和创建时间；绝不公开令牌 hash、一次响应之后的明文、scope 或签发者 secret。受管的 `external_id` 冲突返回带安全元数据的 `409`；与未受管账户的邮箱冲突则返回通用 `409`。

创建频道身份需要 `platform` 和 `external_id`，`name` 可选。该操作直接关联入站消息，不会授予 Web UI 或 OpenID Connect 登录能力。目标必须是由同一管理员创建的活跃普通用户；该管理员此前持有的开通令牌所创建的用户也可以管理。重复的 `(platform, external_id)` 返回 `409`，其他管理员拥有的用户返回 `404`。
