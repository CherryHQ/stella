---
title: Web UI 测试
description: 验证 Stella Web UI 的浏览器自动化流程。
---

使用 `test/e2e/` 下的 Playwright spec 自动化 Stella UI 验证。所有值得保留的场景都必须成为已提交的 spec，保证验证可重复。套件使用端口 `25777` 的一次性 testbed，`mise run test:e2e` 会自动管理它。

## 环境与 fixture setup

需要交互探索时：

```bash
mise run testbed:start
cd test/e2e
bun run playwright test --ui
bun run playwright codegen http://127.0.0.1:25777
mise run testbed:stop
```

testbed 会打印临时凭据路径。凭据是秘密，不要打印或提交。`lib/fixtures.ts` 中的 checked-in fixture 提供 `admin`、`user`、`db` 和 `loginAsAdmin`，spec 应使用它们，不要通过浏览器创建账号。只有注册流程本身是被测对象时，才使用浏览器注册。真实模型测试带有 `@model` 标签，`mise run test:e2e:fast` 会排除它们。

绝不要使用 `~/.stella-dev`、端口 `25678`、手工创建 fixture 账号，或用浏览器/CDP 注册普通测试数据。`testbed:stop` 负责清理；显式 start/stop 流程必须在每个退出路径停止 testbed。

## Common workflows

Playwright 默认运行 functional project。model project 只包含 `@model` 测试并允许一次 retry；非模型测试不重试。可以在 `test/e2e` 中运行单个文件或标题，也可以打开 UI runner：

```bash
cd test/e2e
bun run playwright test mcp/1-catalog.spec.ts
bun run playwright test -g 'catalog endpoint'
bun run playwright test --ui
bun run playwright codegen http://127.0.0.1:25777
```

`codegen` 用于发现 selector 和交互，不用于制造 fixture。把稳定交互复制进 checked-in spec，并断言结果。导航后重新定位，因为 locator 状态和生成的引用都不持久。

## Assertion pattern

每个有意义的操作后都要先验证结果再继续：

1. 用 Playwright `expect` 断言可见文本或 role，包括 heading、row、alert 或 empty state。
2. 如果行为包含导航，断言 URL 或 route。
3. 操作可能失败时，断言错误 banner 和失败的网络响应。
4. 目标是验证 UI 写入内容时，用 `db` 或 `admin` 共同断言。

失败时报告 expected 与 actual，不要静默继续。优先使用语义 role 和 label，避免脆弱 CSS 或只依赖截图。布局或失败需要视觉诊断时使用 screenshot 和 trace，但不要让它们成为唯一断言。

## Seeding UI states

testbed fixture 创建账号并提供已认证 API client。在 spec 中使用 `lib/fixtures.ts` 的 `admin`、`user`、浏览器登录用 `loginAsAdmin`，数据库断言用 `db`：

```ts
const goal = await admin.post("/api/goals", {
  agent_id: agentID,
  title: "Visual goal",
  intent: "…",
});
await loginAsAdmin();
await page.goto("/agents");
await expect(page.getByText("Visual goal")).toBeVisible();
const row = (await db`select lifecycle from agent_goal where id = ${goal.body.id}`)[0];
```

使用 API 创建它能创建的状态，例如 draft goal、MCP server、agent、provider 配置和 scheduler job。只有视觉验证才允许直接在 `db` 中伪造生命周期状态；行为测试必须走真实 HTTP/UI 路径。Work 和 Inbox 由 `agent_goal.lifecycle` 决定：`blocked` 表示需要关注，`active` 表示进行中，接受历史是 `done` 且 `done_reason='accepted'`、`acceptance_state='passed'` 并有非空 `accepted_output`。可重复 workflow 需要 `agent_workflow` 行，`owner_kind='agent'`、`payload_format='frozen/v0'`、children/edges 为空的 payload，以及指向已接受 goal 的 `source_goal_id`。取消有真实 API route，应通过它测试。

不要打印凭据文件或 `.env`。fixture 数据应限定在测试 run 内，并在 fixture teardown 中删除创建的资源。

## Notes

- `testbed:start` 管理内置 PostgreSQL 和服务，不替代 system harness 的进程生命周期覆盖。
- 隐藏 Chrome tab 会节流渲染和 rAF；性能 spec 放在 `test/e2e/perf`，并使用可见浏览器。
- 虚拟化或 `content-visibility` 历史使用 `textContent` 比 `innerText` 安全；拼接文本没有分隔符时应计数，不要依赖含糊的换行或边界正则。
- 导航或 rerender 后 snapshot 和 locator 假设可能失效，应重新定位，不要复用生成的引用。
- 登录表单使用 `username` 和 `password` placeholder，密码至少 8 个字符，通常重定向到 `/agents`。
- 使用 API client 做 `ADMIN_PAT`/`USER_PAT` 语义的权限检查，不要通过浏览器创建 fixture 账号。

DB invariant 应与 `api-test.md` 配合；性能验证使用 `web-perf-test.md`。功能断言证明行为，不证明速度。
