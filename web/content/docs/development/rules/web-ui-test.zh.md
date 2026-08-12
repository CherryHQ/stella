---
title: Web UI 测试
description: 使用浏览器自动化验证 Stella Web UI 的工作流。
---

使用 `tap` 浏览器命令自动化验证 Stella Web UI。

## 环境

基础 URL：`http://localhost:25678`

```bash
URL=${URL:-http://localhost:25678}
```

## Fixture 准备

启动一次性测试实例；它会启动自己的 embedded PostgreSQL、服务，并通过运行中的 HTTP API
创建 fixture 账户。这是长时间运行的命令，应在独立终端或后台任务中运行。

```bash
# 终端 1：测试期间保持运行。
mise run testbed:start
```

如果默认端口 25678 被占用（通常是开发服务在用），直接构建并以其他端口启动
testbed 二进制——URL 和凭据会按新端口打印，`mise run testbed:stop` 仍能清理：

```bash
go build -o dist/bin/testbed ./test/testbed
./dist/bin/testbed start -port 25811
```

start 会打印服务 URL 和临时凭据路径。凭据文件权限为 `0600`，其中有两个账户的身份和角色、
管理员的邮箱/密码/PAT，以及无密码普通用户的 PAT。它是 secret：不要提交、打印或粘贴到共享日志。

结束后，请在启动它的 checkout 中停止。stop 负责优雅关闭，并删除临时服务、数据库和凭据。

```bash
mise run testbed:stop
```

不要使用 `~/.stella-dev`、手动创建 fixture，或用浏览器/CDP 自动化注册 fixture 账户。只有
注册 UI 本身是测试对象时，才使用浏览器注册。

读取凭据时不要把它们显示出来：

```bash
CREDS="<testbed:start 打印的凭据路径>"
ADMIN_EMAIL="$(jq -r '.admin.email' "$CREDS")"
ADMIN_PASSWORD="$(jq -r '.admin.password' "$CREDS")"
ADMIN_PAT="$(jq -r '.admin.token' "$CREDS")"
USER_PAT="$(jq -r '.user.token' "$CREDS")"
```

`tap` CLI 必须已安装且位于 `$PATH`。

## 工具选择（从最省开始）

| 需求              | 工具                                                      |
| ----------------- | --------------------------------------------------------- |
| 检查页面文本/状态 | `tap browser text [selector]`                             |
| 发现可交互元素    | `tap browser snapshot --interactive -f json`              |
| 填表 / 点击按钮   | `tap browser fill` / `tap browser click`                  |
| 运行 JS 断言      | `tap browser evaluate <js>`                               |
| 检查网络响应      | `tap browser network wait --url-pattern "*/api/*" --body` |
| 只验证视觉效果    | `tap browser screenshot`                                  |

优先使用 `text` 和 `snapshot`，只有布局或视觉效果重要时才截图。

## 浏览器生命周期

```bash
# 检查浏览器是否已运行
tap status

# session 过期或不存在时，启动一个新的可见窗口
tap browser open "$URL" --show

# 后续导航会复用 session
tap browser open "$URL"
```

如果 `tap browser` 因连接错误失败，说明 session 已过期：

```bash
rm "$HOME/.cache/tap/browser/state.json"
tap browser open "$URL" --show
```

## 常见工作流

### 登录

```bash
tap browser open "$URL/login"
tap browser snapshot --interactive -f json
# 清除可能预填的值
tap browser evaluate 'document.querySelectorAll("input").forEach(i => { i.value = ""; i.dispatchEvent(new Event("input", {bubbles:true})); })'
tap browser fill @e3 "$ADMIN_EMAIL" @e4 "$ADMIN_PASSWORD" --submit @e1
sleep 1
tap browser text | head -20
```

如果 fill+submit 之后页面仍停在 `/login`，说明受控的 React 输入框忽略了 CDP
填入的值。改为在页面内通过 API 登录（session cookie 的设置方式相同），然后导航：

```bash
tap browser evaluate "fetch('/api/auth/local/login',{method:'POST',headers:{'Content-Type':'application/json'},credentials:'include',body:JSON.stringify({email:'$ADMIN_EMAIL',password:'$ADMIN_PASSWORD'})}).then(r=>r.status)"
tap browser open "$URL/agents"
```

### 注册（仅在测试注册 UI 时）

```bash
# 不要用它创建 fixture。只有注册流程本身是测试对象时，才从登录页点击 Register。
tap browser click @e2   # "Need an account? Register"
tap browser snapshot --interactive -f json
tap browser fill @e3 "$USERNAME" @e4 "$PASSWORD" @e5 "$PASSWORD" --submit @e1
sleep 1
tap browser text | head -20
```

### 验证页面已加载

```bash
tap browser text | head -30
tap browser snapshot --interactive
```

### 导航

```bash
tap browser open "$URL/agents"
tap browser open "$URL/settings"
```

## 断言模式

每个操作后先验证结果，再继续：

1. **文本检查**：`tap browser text | head -N`，确认预期标题或内容。
2. **URL 检查**：`tap status --json`，确认导航落在正确页面。
3. **错误检查**：在 `tap browser text` 输出中查找错误横幅。

断言失败时，报告期望值和实际值；不要静默继续。

## 构造 UI 状态

testbed 没有配置模型 provider，goal 不会自行推进：它们停留在 `draft`，`activate`
返回 409（"plan gate not satisfied"）。普通 fixture 走 API 创建；依赖 lifecycle 的
状态直接写 embedded 数据库。数据库改写**只用于视觉验证**——行为测试必须通过真实
路径到达状态。

```bash
# API 可创建：draft goal 和 scheduler job
curl -X POST -H "Authorization: Bearer $ADMIN_PAT" -H 'Content-Type: application/json' \
  "$URL/api/goals" -d '{"agent_id":"stella","title":"…","intent":"…"}'
curl -X POST -H "Authorization: Bearer $ADMIN_PAT" -H 'Content-Type: application/json' \
  "$URL/api/agents/stella/scheduler/jobs" \
  -d '{"name":"…","cron":"0 8 * * *","message":"…"}'   # 或 "every": "4h"

# 数据库访问：DSN 取自运行中的进程，psql 在托管的 pg runtime 里
DSN=$(ps -wwE -p "$(lsof -t -iTCP:25811 -sTCP:LISTEN)" -o command= \
  | tr ' ' '\n' | grep -m1 STELLA_DATABASE_URL | cut -d= -f2-)
PSQL=("$HOME"/.stella/pg-runtime/*/downloaded/postgresapp/postgres/bin/psql)
"$PSQL" "$DSN" -c "…"
```

Work UI 和 Inbox 依据的状态都在 `agent_goal` 表：

- **Needs you / Inbox 条目**：`lifecycle='blocked'`（任意 `block_reason`，如 `budget_exhausted`）。`needs_attention` 是推导值，不是列。
- **Active**：`lifecycle='active'`。
- **已验收的历史**：`lifecycle='done', done_reason='accepted'`——check 约束还要求 `acceptance_state='passed'` 且 `accepted_output` 非空。
- **已取消的历史**：`POST /api/goals/{id}/cancel` 即可，无需写库。
- **Repeatable**：向 `agent_workflow` 插入一行，`owner_kind='agent'`、`payload_format='frozen/v0'`、`payload='{"children":[],"edges":[]}'::jsonb`，`source_goal_id` 指向已验收的 goal。

## 相关文档

本页覆盖浏览器层。要断言 UI 操作实际写入了什么，可配合 `api-test.md` 中的数据库检查：
此处驱动浏览器、那里断言数据库，便构成完整的 `browser -> API -> DB` e2e。只测后端行为时，
直接用 `api-test.md`，不需要浏览器。性能测量（帧时间、按键开销、加载/传输成本）使用
`web-perf-test.md` 的 harness；本页的功能检查不证明性能。

浏览器只用于验证 UI 行为。API 和基于角色的访问控制检查应使用 `ADMIN_PAT` 或 `USER_PAT`，
不要通过浏览器自动化创建 fixture 账户。

## 说明

- 导航后 snapshot ref（`@e1`、`@e2` 等）会失效，必须重新 snapshot。
- `tap browser screenshot <path>` 可能忽略路径参数，把 `screenshot-*.png` 写到当前目录——之后自行移动文件。
- 密码最短长度为 8 个字符。
- 登录表单使用 `username`、`password` placeholder。
- 登录后应用会跳转至 `/agents`。
