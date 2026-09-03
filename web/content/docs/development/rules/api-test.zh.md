---
title: 后端 API 测试
description: 基于 testbed 的实时 HTTP API 与数据库断言。
---

手工 API 检查使用一次性 testbed。它管理内置 PostgreSQL，并打印权限为 `0600` 的凭据文件。

```bash
mise run build
mise run testbed:start
STATE=test/e2e/test-results/testbed.json
CREDS=$(jq -r .credentialsPath "$STATE")
URL=$(jq -r .baseURL "$STATE")
TOKEN=$(jq -r .admin.token "$CREDS")
DATABASE_URL=$(jq -r .database_url "$CREDS")
curl -H "Authorization: Bearer $TOKEN" "$URL/api/auth/me"
psql "$DATABASE_URL" -c 'select 1'
mise run testbed:stop
```

不要使用 `~/.stella-dev`、端口 `25678`、手工账号或外部 PostgreSQL。异步接口要轮询到终态再断言。时间统一使用 UTC，绝不要打印凭据。
