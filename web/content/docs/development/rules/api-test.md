---
title: Backend API testing
description: Live HTTP API and database assertions against testbed.
---

Use the disposable testbed for manual API checks. It owns embedded PostgreSQL and prints a `0600` credentials file.

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

Do not use `~/.stella-dev`, port `25678`, hand-created accounts, or an external PostgreSQL server for this workflow. Poll asynchronous endpoints before asserting. Store and compare timestamps in UTC, and never print credentials.
