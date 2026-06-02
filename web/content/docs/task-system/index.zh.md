---
title: 任务系统
---

Stella 的任务系统用于追踪不应该只停留在一条聊天消息里的工作。

当一个 goal 需要多个步骤、依赖关系、blocker、运行历史或人工 review 时，就使用任务系统。Stella 可以在后台执行单个 task，并把 task 状态汇总到 goal。它现在**不会**自动把 goal 拆成子任务：你需要显式创建 task，把它们挂到 goal 下，并在需要顺序时添加依赖。

先阅读[任务系统概览](/docs/task-system/overview)。
