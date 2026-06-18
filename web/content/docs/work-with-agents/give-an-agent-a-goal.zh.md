---
title: 给 Agent 一个 goal
---

使用 Stella 应该像向一个靠谱同事求助。

你不需要知道 Agent 会使用哪个 tool、API、制度文档或工作流。把 goal、相关上下文和约束告诉它。Stella 会为 Agent 准备 memory、knowledge、skills、tools 和 workspace context。

## 写清楚结果

好的例子：

> 为这次客户晚餐准备报销材料。告诉我还缺什么；如果有例外项，请创建财务 review 工作。

弱的例子：

> 帮我处理费用。

Agent 可以追问，但清楚的结果会节省时间。

## 加上约束

有用的约束：

- 截止时间。
- 输出格式。
- 审批边界。
- 谁需要 review。
- 应使用哪些来源文档。
- Agent 不能做什么。

## 让 Agent 使用自己的上下文

共享 Agent 自带上下文：

- 业务负责人写的 instructions。
- 工作流相关 knowledge。
- 可复用方法 skills。
- 外部行动 tools。
- 关于你和你偏好的 memory。

所以你可以正常提出请求，而不是手动驱动每一步。

## 大 goal 使用任务系统

如果 goal 有多个步骤，可以让 Agent 用「带 plan 的 goal」来追踪它：

> 为这件事创建一个带多步 plan 的 goal——列出步骤、依赖关系和需要的人工 review 节点——然后运行它。

Agent 编写 plan，系统把它物化成子 tasks（你不能手动把 task 挂到 goal 上）。单步 goal 创建即运行；多步 goal 先规划、再激活。用聊天提供上下文和决策，用任务 UI 查看执行状态。本版本不包含自动 LLM goal 拆分，所以由 Agent 显式编写 plan。
