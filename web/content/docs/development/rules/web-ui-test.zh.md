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

先启动一个隔离的开发实例，并确保所用 `STELLA_HOME` 中没有用户，再通过运行中的
HTTP API 创建 fixture 账户。helper 会等待 `/readyz`；它不会启动服务，也不会直接写数据库。

```bash
# 终端 1：按常规方式启动开发服务，默认使用 ~/.stella-dev。
mise run dev

# 终端 2：创建一个本地管理员和一个无密码普通用户。
mise run agent-test:bootstrap
```

helper 会原子写入 `$STELLA_HOME/agent-test-credentials.json`（默认是
`~/.stella-dev/agent-test-credentials.json`），文件权限为 `0600`。其中有两个账户的
身份和角色、管理员的邮箱/密码/PAT，以及无密码普通用户的 PAT。它是 secret：不要提交、
打印或粘贴到共享日志。

同一基础 URL 下已有有效 artifact 时，helper 会安全复用，绝不会覆盖。如果 artifact
写入前 bootstrap 中断，请丢弃该隔离的 `STELLA_HOME` 并重新启动实例；first-user 注册在
部分初始化的实例上不能安全重放。

读取凭据时不要把它们显示出来：

```bash
CREDS="${STELLA_HOME:-$HOME/.stella-dev}/agent-test-credentials.json"
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

## 相关文档

本页覆盖浏览器层。要断言 UI 操作实际写入了什么，可配合 `api-test.md` 中的数据库检查：
此处驱动浏览器、那里断言数据库，便构成完整的 `browser -> API -> DB` e2e。只测后端行为时，
直接用 `api-test.md`，不需要浏览器。性能测量（帧时间、按键开销、加载/传输成本）使用
`web-perf-test.md` 的 harness；本页的功能检查不证明性能。

浏览器只用于验证 UI 行为。API 和基于角色的访问控制检查应使用 `ADMIN_PAT` 或 `USER_PAT`，
不要通过浏览器自动化创建 fixture 账户。

## 说明

- 导航后 snapshot ref（`@e1`、`@e2` 等）会失效，必须重新 snapshot。
- 密码最短长度为 8 个字符。
- 登录表单使用 `username`、`password` placeholder。
- 登录后应用会跳转至 `/agents`。
