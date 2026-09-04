---
title: 扩展 Stella
---

当内置 Agent、任务系统和阅读流程需要自定义能力时，可以扩展 Stella。

开发者可以添加 provider adapter、沙箱后端、managed channel、skill 与 manifest 支持的 CLI 集成。每类扩展使用自己的最小契约，Stella 没有万能 plugin runtime。面向用户的工作流放在 Agent 文档里；这一节只放实现细节。
