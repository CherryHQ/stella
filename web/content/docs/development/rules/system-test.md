---
title: System testing
description: The five test layers used by Stella.
---

Choose the lowest layer that can reach the seam. The system suite is the process-seam layer: it boots the real `stellad` subprocess over TCP with embedded PostgreSQL and the shared scripted fake provider.

| Layer       | What runs                                              | Launcher       | Command                            | Browser |
| ----------- | ------------------------------------------------------ | -------------- | ---------------------------------- | ------- |
| In-process  | Go tests and live DB                                   | Go test        | `mise run test`                    | no      |
| System      | Real subprocess, TCP, SSE, restart and workers         | system harness | `mise run system-test`             | no      |
| Browser E2E | UI, API, DB and functional flows                       | `test/testbed` | `mise run test:e2e`                | yes     |
| Perf        | Playwright measurements against testbed and fake model | perf project   | `mise run perf:measure -- <label>` | yes     |
| Eval        | Agent behavior benchmark                               | Harbor         | `mise run eval:loop`               | no      |

The system harness keeps its own process control instead of using testbed because it must own process groups, forced kills, restart identity, and startup failure detection. Testbed is intentionally a disposable application fixture, not a process-lifecycle oracle.

Run `mise run system-test`. It downloads the supported embedded PostgreSQL runtime, builds `dist/bin/stellad`, runs ordered journeys, and cleans up the subprocess and database. Every request to the fake provider stays on loopback.

Add a system journey only for process startup, real HTTP auth, streaming transport, cross-request flows, or asynchronous workers. Functional browser coverage belongs in `test/e2e`; performance scenarios belong in `test/e2e/perf`. Pure logic and handler behavior remain in-process tests.
