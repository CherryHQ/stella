---
title: 核心概念
---

这些概念会贯穿 Stella。

## Tenant

Tenant 是组织边界。它把用户、Agent、凭证和数据限制在正确的组织内。

## User

User 是和 Agent 协作的人。用户可以从 Web UI、终端，或 Telegram、Discord、QQ、飞书、微信等渠道聊天。

## Agent

Agent 是共享的专业伙伴。它拥有自己的角色、instructions、模型设置、skills、tools、knowledge、工作区、渠道绑定和记忆策略。

围绕工作创建 Agent，而不是围绕技术集成创建 Agent。`财务报销 Agent` 比 `表格 bot` 更好。

## Session

Session 是用户和 Agent 之间的一次持续协作。它保存对话上下文和工作区状态，让工作可以继续，而不是每条消息都重新开始。

## Memory

Memory 让 Agent 能长期认识一个人。Stella 支持每用户、每 Agent 的独立记忆，所以同一个 HR Agent 可以用不同上下文理解 Alice 和 Bob。

当你希望多个 Agent 共享同一套用户偏好时，Stella 也可以使用共享用户记忆。

## Knowledge

Knowledge 是 Agent 的专业参考资料：制度、流程文档、示例、playbook、PDF、保存的文章，或团队专属上下文。

Knowledge 回答的问题是：这个 Agent 为了把工作做好，需要知道什么？

## Skills

Skills 是可复用的工作方法。一个 skill 可以教 Agent 如何做代码审查、准备报告、处理事故、筛选候选人，或执行财务 checklist。

Skills 不只是 prompt。它们打包 instructions、工具使用方式和工作流约定。

## Tools

Tools 是 Agent 可以调用的外部能力：命令行工具、API、OAuth 连接服务、通知渠道、文件操作，以及 plugin 提供的函数。

Tools 回答的问题是：这个 Agent 实际能做什么？

## Goal

Goal 是用户交给 Agent 的目标。好的 goal 描述期望结果，而不是列出所有实现步骤。Stella 会把每个 goal 追踪为一棵子目标树，包括依赖关系、blocker、运行记录和 review 状态，并持续推进直到验收通过。

## 工作如何组织

在 Stella 里，承载工作的概念只有两个：

- **Session**：工作发生的地方——上下文、对话和执行，此时此地。
- **Goal**：持久的目标结果——跨重启追踪，直到验收通过。

其余一切都是作用在 Session 或 Goal 上的动作，而不是第三种工作形态：

- **委托（Delegate）**：把一个聚焦的子问题交给隔离的 Session 处理，保持主对话干净。
- **回忆（Memory 检索）**：跨历史 Session 搜索，上下文永不丢失。Memory 本身也是一个独立功能——你可以在设置里查看和编辑 Agent 记住的内容。
- **Workflow**：把一个验收通过的 Goal 保存下来，之后换新输入再跑一次。
- **Schedule**：给对话或 Workflow 加一个时间触发器——稍后执行，或每天早上执行。Schedule 是触发器，不是工作本身。

所以当你说"保存这个，每天早上跑一次"时，Agent 会把验收通过的 Goal 存成 Workflow，再给它加上 Schedule。

Web UI 也按这个划分组织。每个 Agent 有两个空间：

- **对话（Conversations）**——侧边栏里的话题列表，以及点击标题打开的那个页面：你和这个 Agent 的所有话题。话题运行时会显示状态；如果它在别处完成，会保留“待查看”标记，打开后清除。
- **工作（Work）**——这个 Agent 正在追踪到结果的所有事情，按你需要的顺序呈现：**需要你处理**、**进行中**、**定时任务**、**可重复**（你保存的 Workflow），以及**历史**。

**收件箱（Inbox）** 是同一个"需要你处理"，只是范围更大一层：它汇总所有 Agent 中等你处理的事项，你不用挨个去看。

## Review

Review 是人工检查点。Agent 可以执行工作，但组织仍然可以把判断、审批和责任留在正确的人手里。
