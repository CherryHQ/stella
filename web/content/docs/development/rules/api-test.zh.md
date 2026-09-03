---
title: 后端 API 测试
description: 针对一次性 testbed 和 PostgreSQL 的实时后端 HTTP API 集成测试流程。
---

针对运行中的 testbed 驱动后端 HTTP API，并在其 PostgreSQL 数据库中断言。这是 API/集成测试，不是浏览器 E2E。适用于只有实时服务才能表现的行为：后台 worker、goal dispatcher、scheduler tick、真实模型调用，或 Go 测试无法触及的多请求流程。确定性逻辑放在 Go 测试（`mise run test`）。

自动化形状是 **testbed -> bearer token -> HTTP API -> DB 断言 -> 清理**。可重复的子进程 journey 见 `system-test.md`，浏览器覆盖见 `web-ui-test.md`。

## 环境

托管 testbed 使用 `http://127.0.0.1:25777`、内置 PostgreSQL 和权限为 `0600` 的凭据文件：

```bash
mise run build
mise run testbed:start
STATE=test/e2e/test-results/testbed.json
CREDS=$(jq -r .credentialsPath "$STATE")
URL=$(jq -r .baseURL "$STATE")
TOKEN=$(jq -r .admin.token "$CREDS")
DATABASE_URL=$(jq -r .database_url "$CREDS")
```

checked-in 测试优先使用 `lib/fixtures.ts` 和 `lib/api.ts`，不要自行解析凭据。手工检查可以使用：

```bash
curl -H "Authorization: Bearer $TOKEN" "$URL/api/auth/me"
psql "$DATABASE_URL" -c 'select 1'
```

不要使用 `~/.stella-dev`、端口 `25678`、手工账号或外部 PostgreSQL。绝不要打印凭据或 `.env` 值。

## 1. 驱动 API

调用 UI 和 fixture 使用的相同 route，并从响应中保存 ID：

```bash
curl -s -X POST "$URL/api/<resource>" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{ ... }'
```

TypeScript spec 使用 `test/e2e/lib/api.ts` 的 typed helper 和 `admin`/`user` fixture。异步功能要轮询读取 endpoint，直到终态或超时，不能假设第一次 GET 已经反映写入。

## 2. DB 断言

API 响应覆盖 happy path；对 API 不暴露的 invariant 查询 PostgreSQL，例如 orphan row、archived 标记、计数和 FK integrity。记录 UTC 的 run-start，只比较本次 run 创建的行：

```bash
psql "$DATABASE_URL" -c "select ... from <table> where user_id = '<uid>' and created_at > '<run-start-utc>'"
```

失败时报告 expected 与 actual，不要静默继续。

## 3. 清理

手工运行创建的实体要取消或删除，并从同一 checkout 停止 testbed：

```bash
curl -s -X POST -H "Authorization: Bearer $TOKEN" "$URL/api/<resource>/<id>/cancel"
mise run testbed:stop
```

fixture 必须在测试失败时也清理 fake server、agent、MCP server 和其他资源。

## Gotchas

- **某些 shell 中 `GID` 是整数变量。** `GID="019f..."` 可能报 `bad math expression`；UUID 变量使用 `GOAL` 或其他名称。
- **Session 存在 `ctx_conversation`。** 字段是 `session_id`、`kind`、`archived`，不是 `session` 表。许多功能使用 `ON DELETE RESTRICT`，回滚写入可能留下 orphan，而不是级联删除。
- **存储和序列化都使用 UTC。** `created_at` 必须与 UTC 的 run-start 比较。
- **后台工作使用 River queue。** 例如 `stella_goal_tick`；断言前给 dispatcher 几个 tick，约每次 2 秒。

## Worked example: goal lifecycle + agent review

下面的 testbed 示例驱动 decomposition -> execution -> `needs_verdict` judgment item 的 agent auto-review -> acceptance。步骤和轮询语义必须保持明确：

```bash
# 创建一个 judgment item 会停在 needs_verdict 的 goal。
# agent-judgment contract 放在 leaf 上，不要放在 composite 上。
curl -s -X POST "$URL/api/goals" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{
    "agent_id": "<model-configured-agent>",
    "title": "E2E review smoke",
    "intent": "Produce a short friendly greeting in English. Done means a one-sentence greeting exists.",
    "acceptance_contract": {"policy":"deterministic_then_judgment","items":[
      {"id":"quality","kind":"judgment","authority":"agent","required":true,
       "rubric":"Does the output contain a clear, friendly one-sentence greeting in English?"}]}
  }'
# 保存响应中的 id、session_id 和 user_id。

# 轮询到 leaf 的 acceptance_state=passed，通常约 30 秒。
curl -s -H "Authorization: Bearer $TOKEN" "$URL/api/goals/<goal-id>/children"
```

对应的 checked-in TypeScript 使用 `admin.post`，随后反复调用 `admin.get('/api/goals/<id>/children')`，在有界轮询中等待终态。不要用固定 sleep 替代轮询。

断言：

1. leaf 达到 `acceptance_state: passed`；日志包含 `tool=goal_control`，顺序为 `decompose` -> `submit` -> `verdict`，且 `pass=true`。
2. **不能发生多余的 session disposal**：本次 run 创建的每个 session 都保持 `archived = false`；成功提交不能触发补偿性清理。

   ```sql
   select left(session_id,18) sid, kind, archived
   from ctx_conversation
   where user_id = '<user-id>' and created_at > '<run-start-utc>' order by created_at;
   -- 预期 root / decompose / execute / review 行，全部 archived = f
   ```

3. rollback 时的 disposal 已由 `TestReview_DisposesSessionOnRollback` 和 `TestDisposeOnRollback` 确定性覆盖；强行制造线上 `uniq_agent_goal_active_attempt` race 脆弱且收益很低。
