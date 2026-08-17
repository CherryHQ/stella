---
title: 通过企业集成开通用户
description: 使用受限的开通令牌创建和管理无密码、仅 API 可用的用户。
---

当企业目录或员工系统需要创建 Stella 用户、但不应获得管理员权限时，使用此 API。

## 创建开通令牌

以管理员身份登录 Web UI，打开 **设置 → 用户开通**，然后选择 **创建令牌**。填写集成名称、选择有效期，并将返回的 `stella_prv_…` 复制到集成使用的密钥管理器。Stella 只显示一次。

Web UI 使用当前交互式管理员 session。等价的 API 请求是：

```sh
curl -X POST https://stella.example/api/admin/provisioning-tokens \
  -H 'Content-Type: application/json' \
  -b 'stella_session=…' \
  -d '{"name":"hr-provisioner"}'
```

令牌默认有效期为 90 天，最长 365 天。计划轮换时可短暂保留两个令牌：部署并验证新令牌后，在 **设置 → 用户开通** 中撤销旧令牌。

开通令牌只能访问开通用户 API；不能登录 Web UI、管理普通账户或创建更多开通令牌。

## 开通和轮换用户

使用目录中的不可变标识创建用户。响应会仅一次返回无密码 `stella_pat_…` bearer token。

```sh
curl -X POST https://stella.example/api/provisioned-users \
  -H 'Authorization: Bearer stella_prv_…' \
  -H 'Content-Type: application/json' \
  -d '{"external_id":"employee-42","email":"ada@example.com","name":"Ada","token_name":"hr-sync"}'
```

使用 `GET /api/provisioned-users` 和 `GET /api/provisioned-users/{id}` 读取安全元数据。令牌遗失或泄露时，调用 `POST /api/provisioned-users/{id}/rotate-token`：它先撤销此前由开通流程签发的令牌，再创建一个替代令牌；不会撤销用户独立创建的个人令牌。

所有用户令牌默认 90 天过期；`expires_at` 可以缩短或延长，但最长 365 天。没有永不过期选项。

## 关联消息平台身份

要把目录中已有账户的频道消息路由到受管用户，请使用平台的稳定用户标识创建频道身份：

```sh
curl -X POST https://stella.example/api/provisioned-users/PROVISIONED_USER_ID/channel-identities \
  -H 'Authorization: Bearer stella_prv_…' \
  -H 'Content-Type: application/json' \
  -d '{"platform":"feishu","external_id":"on_union_1","name":"Ada"}'
```

该操作只创建消息平台身份，不会允许此账户登录 Web UI。同一管理员持有的替换开通令牌可以继续管理旧令牌创建的用户；其他管理员的令牌会收到 `404`，已提升或已停用的用户会收到 `403`。

## 恢复与事件响应

若 `external_id` 已存在于受管用户，创建会返回 `409`，其中只有安全的既有用户和令牌元数据，绝不包含旧令牌。将其视为重试/对账结果：读取资源，若之前响应丢失则轮换令牌。若邮箱与未受管账户冲突，也会返回 `409`，但不会透露该账户的任何信息。

集成令牌或用户令牌泄露时应立即轮换。需要终止人员访问时，调用 `POST /api/provisioned-users/{id}/deactivate`。停用会走 Stella 的正常账户锁定流程，撤销 session 和全部个人访问令牌；此 API 不能重新启用。若受管用户被提升为管理员，开通方只能读取它；请调查后使用交互式管理员 session 操作。
