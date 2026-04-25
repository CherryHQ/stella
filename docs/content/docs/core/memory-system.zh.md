---
title: 内存系统
---

## 概述

内存系统是**基于插件**的。`pkg/memory/` 中的 `memory.Provider` 接口定义了契约，具体实现位于 `plugins/memory/`。anna 内置了两个插件：

| 插件       | 包路径                   | 默认启用 | 说明                                                         |
| ---------- | ------------------------ | -------- | ------------------------------------------------------------ |
| **LCM**    | `plugins/memory/lcm/`    | 是       | 无损上下文管理 —— 摘要 DAG、压缩、搜索、探索                 |
| **Simple** | `plugins/memory/simple/` | 否       | 滑动窗口 —— 保留最近 N 条消息在 token 预算内，无摘要         |

### 切换插件

内存插件的管理方式与其他插件相同。在管理面板或通过 `anna plugin` 命令：

```bash
anna plugin disable memory/lcm
anna plugin enable memory/simple
```

同时只能启用一个内存插件。两者使用相同的底层 `ctx_messages` 表，因此切换插件会保留已存储的消息。

## Provider 接口

核心 `Provider` 接口（`pkg/memory/provider.go`）包含 5 个方法：

| 方法                                        | 说明                                                       |
| ------------------------------------------- | ---------------------------------------------------------- |
| `Bootstrap(ctx, session)`                   | 确保会话的对话记录存在                                     |
| `Append(ctx, session, msgs)`                | 持久化消息并添加上下文项                                   |
| `Assemble(ctx, session, budget, freshTail)` | 在 token 预算内构建上下文，返回 `[]ai.Message`               |
| `Stats(ctx, session)`                       | 返回会话统计（token 数、消息数）                           |
| `Close()`                                   | 释放资源                                                   |

### 可选能力

Provider 可以通过类型断言实现额外的接口来扩展能力：

| 接口               | 方法                                                   | 说明                         |
| ------------------ | ------------------------------------------------------ | ---------------------------- |
| `Compactor`        | `NeedsCompaction`, `Compact`                           | 上下文窗口压缩               |
| `Searcher`         | `Search`                                               | 跨消息和摘要的全文搜索       |
| `Explorer`         | `Describe`, `Expand`                                   | 检查和深入摘要               |
| `ProfileStore`     | `GetProfile`, `SetProfile`                             | 每用户每代理的持久化笔记     |
| `SessionManager`   | `SaveInfo`, `LoadInfo`, `ListInfo`, `LoadHistory`      | 会话元数据和历史管理         |
| `ReviewSource`     | `ListUnreviewed`, `BuildReviewContext`, `MarkReviewed` | 自我改进评审数据             |

LCM 插件实现了全部 7 个接口。Simple 插件实现了 `Provider`、`ProfileStore` 和 `SessionManager`。

## 工具自动生成

`memory.BuildTool(provider)` 检查 provider 的能力并生成匹配动作的 `tools.Tool`：

| 动作             | 需要接口       | 说明                           |
| ---------------- | -------------- | ------------------------------ |
| `status`         | （始终可用）   | 显示会话统计（token、消息）    |
| `search`         | `Searcher`     | 按模式搜索消息和摘要           |
| `describe`       | `Explorer`     | 检查摘要的元数据和血统         |
| `expand`         | `Explorer`     | 深入压缩后的摘要               |
| `profile_get`    | `ProfileStore` | 读取每用户的持久化笔记         |
| `profile_update` | `ProfileStore` | 更新每用户的持久化笔记         |

工具的 JSON schema、描述和调度都是动态适配的。能力较少的 provider 会生成动作较少的工具。

## LCM 插件

### 架构

```
ai.Message (user/assistant/tool_result)
        |
        v
  +----------+     Append     +-----------+
  | Provider  | ------------> | SQLite DB |
  +----------+                +-----+-----+
     |    |                          |
     |    | Compact                  |  Tables:
     |    v                          |    ctx_conversations
     | +------------------+          |    ctx_messages
     | | CompactionEngine | <--------+    ctx_summaries
     | +------------------+          |    ctx_items
     |                               |    ctx_summary_messages
     |  Assemble (budget)            |    ctx_summary_parents
     v                               |
  +------------+                     |
  | Assembler  | <-------------------+
  +------------+
        |
        v
  []ai.Message (fresh tail + summaries within token budget)
        |
        v
  LLM context window
```

### 压缩

压缩通过摘要旧消息和摘要来减少上下文窗口。

**模式：**

| 模式          | 行为                                                         |
| ------------- | ------------------------------------------------------------ |
| `Incremental` | 单次叶子遍历 + 一次聚合遍历。当上下文超过阈值时自动运行。    |
| `Full`        | 重复叶子 + 聚合遍历，直到无法进一步压缩（最多 10 次迭代）。  |

**遍历：**

1. **叶子遍历** —— 将新鲜尾部之外的连续消息上下文项分组。10 条以上的消息组被摘要成 `leaf` 摘要（深度 0）。
2. **聚合遍历** —— 将相同深度的连续摘要上下文项分组。2 个以上的摘要组被聚合成 `condensed` 摘要（深度+1）。

**摘要升级：**

1. **普通模式** —— 保留关键决策、理由、约束。目标：input_tokens/3。
2. **激进模式** —— 仅保留持久事实。当普通模式超过目标 150% 时触发。
3. **确定性回退** —— 在句子边界截断。当激进模式仍超过 150% 时触发。

### 上下文组装

1. 将上下文项分为**新鲜尾部**（最后 N 个消息项，默认 20）和**较旧**项。
2. 将新鲜尾部项解析为 `ai.Message` —— 无论预算如何都始终包含。
3. 用较旧的项填充剩余预算，最新优先。
4. 返回较旧事件（按时间顺序）+ 尾部事件。

### 并发

- **每会话互斥锁** —— `Append` 和 `Compact` 获取每会话锁以防止并发修改。
- **对话 ID 缓存** —— 不可变的 `sessionID -> convID` 映射在首次查找后缓存。

## Simple 插件

Simple 插件使用滑动窗口方式：

1. **Append** 将消息存储在 `ctx_messages` 中（与 LCM 相同的 schema）。
2. **Assemble** 返回最近 N 条符合 token 预算的消息，始终保留 freshTail。
3. 无摘要、无压缩、无搜索、无探索。

适用于短会话或资源受限环境。

## 数据库

- **位置：** `~/.anna/anna.db`
- **驱动：** `modernc.org/sqlite`（纯 Go，无 CGO）
- **模式：** WAL，启用外键
- **迁移：** Atlas 生成，通过 `MigrationsFS` 嵌入，启动时自动应用。

**Schema：**

| 表                     | 用途                                                           |
| ---------------------- | -------------------------------------------------------------- |
| `ctx_conversations`    | 每会话一条（`session_id` -> `id` 映射），包含 agent/user ID    |
| `ctx_messages`         | 原始消息，包含 `role`、`content`、`token_count`、顺序 `seq`    |
| `ctx_summaries`        | 摘要 DAG 节点：`kind`、`depth`、`content`、token 统计、时间范围 |
| `ctx_items`            | 有序上下文窗口：指向消息或摘要                                 |
| `ctx_summary_messages` | 将叶子摘要链接到源消息                                         |
| `ctx_summary_parents`  | 将聚合摘要链接到父摘要（DAG 边）                               |
| `ctx_agent_memory`     | 每用户每代理持久化笔记（ProfileStore 使用）                    |

## 配置默认值

| 常量                      | 值   | 说明                   |
| ------------------------- | ---- | ---------------------- |
| `DefaultFreshTail`        | 20   | 受保护免于压缩的消息   |
| `DefaultContextThreshold` | 0.75 | 触发压缩的预算分数     |
| `DefaultLeafChunkSize`    | 10   | 每个叶子摘要的最小消息数 |

---

## 身份与用户记忆

### 三层系统提示

| 层              | 默认来源                        | 文件覆盖                       | 说明                                           |
| --------------- | ------------------------------- | ------------------------------ | ---------------------------------------------- |
| **基础**        | 内置系统指令                    | 代理工作区中的 `SYSTEM.md`     | LLM 的核心行为指令                             |
| **代理灵魂**    | `settings_agents.system_prompt` | 代理工作区中的 `SOUL.md`       | 代理身份、个性和语气                           |
| **用户记忆**    | `ctx_agent_memory.content`      | （无 —— 始终来自数据库）       | 每用户每代理笔记，通过 ProfileStore 注入       |

### 用户记忆

用户记忆存储在 `ctx_agent_memory` 中，键为 `(user_id, agent_id)`。代理通过内存工具的 `profile_update` 动作更新它，通过 `profile_get` 读取它。

### 代理工作区

每个代理在 `$ANNA_HOME/workspaces/{agent_id}/` 有自己的工作区，用于文件覆盖、技能和每代理数据。
