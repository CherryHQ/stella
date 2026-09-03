---
title: Web UI testing
description: Playwright browser workflow for Stella.
---

Checked-in browser scenarios live under `test/e2e/` and run against a disposable testbed on port `25777`.

```bash
mise run testbed:start
cd test/e2e
bun run playwright test --ui
bun run playwright codegen http://127.0.0.1:25777
mise run testbed:stop
```

The testbed prints a temporary credentials path. Treat it as secret, do not print or commit it. Use the credentials for API fixtures and login through the UI only when login or registration is under test. Never use `~/.stella-dev`, manual fixture accounts, or port `25678`.

`mise run test:e2e` runs the functional project and starts/stops the testbed automatically. Real-model tests require `OPENAI_API_KEY`, `OPENAI_BASE_URL`, and `OPENAI_MODEL` in the repository `.env`. Use `test:e2e:fast` to exclude titles tagged `@model`.
