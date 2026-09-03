---
title: Web UI 测试
description: 使用 Playwright 测试 Stella 浏览器界面。
---

浏览器场景放在 `test/e2e/`，使用端口 `25777` 的一次性 testbed。

```bash
mise run testbed:start
cd test/e2e
bun run playwright test --ui
bun run playwright codegen http://127.0.0.1:25777
mise run testbed:stop
```

testbed 会打印临时凭据路径。凭据是秘密，不要打印或提交。只有登录或注册本身是被测对象时才用浏览器操作它。不要使用 `~/.stella-dev`、手工 fixture 账号或端口 `25678`。

`mise run test:e2e` 运行功能项目并自动管理 testbed。真实模型测试需要仓库 `.env` 中的 `OPENAI_API_KEY`、`OPENAI_BASE_URL` 和 `OPENAI_MODEL`。`test:e2e:fast` 会排除标题带 `@model` 的测试。
