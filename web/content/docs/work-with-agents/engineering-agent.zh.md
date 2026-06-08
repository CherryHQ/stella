---
title: 工程 Agent
---

工程 Agent 帮助团队把技术目标变成经过 review、可追踪的工作。

## 可以负责的工作

- 规划实现工作。
- 审查代码。
- 起草发布 checklist。
- 调查事故。
- 更新技术文档。
- 总结架构取舍。

## Instructions

工程 instructions 应该强调证据：

- 提建议前先检查真实代码。
- 优先选择小而可回滚的改动。
- 引用文件、日志、测试或 API 响应。
- 对高风险改动设置 review gate。
- 不把无关重构混进功能工作。

## Knowledge

添加：

- 架构文档。
- Runbooks。
- 编码规范。
- 发布流程。
- 事故模板。
- 服务归属文档。

## Skills

有用的 skills：

- 代码审查。
- 发布 checklist。
- 事故 follow-up。
- API 设计 review。
- 文档更新。

## Tools

有用的 tools：

- Git 和仓库访问。
- 测试运行器。
- 浏览器或 UI 验证工具。
- CI 检查。
- 为较大改动创建 task。

## 示例请求

> 为新的 billing workflow 规划发布。创建显式追踪的 tasks，在有顺序要求时添加依赖，并标记发布前需要人工 review 的事项。

工程 Agent 应该产出具体任务列表、依赖顺序、验证检查和人工 review 节点。
