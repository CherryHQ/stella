---
title: 内存系统
---

## 无损上下文管理（LCM）

### 概述

内存系统为 anna 提供无损上下文管理。每条消息都持久化在 SQLite 数据库中，并组织成摘要的 DAG（有向无环图）。当对话变得太长时，旧消息被压缩成叶子摘要，叶子摘要组又进一步压缩成更高级别的摘要。代理可以深入任何摘要以恢复原始细节 -- 没有任何内容被删除。

包：`internal/memory/`（核心）+ `internal/memory/tool/`（代理工具包装器）。

### 架构

```
ai.Message (user/assistant/tool_result)
        |
        v
  +----------+     ingest      +-----------+
  |  Engine   | -------------> | SQLite DB |
  +----------+                 +-----+-----+
     |    |                          |
     |    | compact                  |  表：
     |    v                          |    ctx_conversations
     | +------------------+          |    ctx_messages
     | | CompactionEngine | <--------+    ctx_summaries
     | +------------------+          |    ctx_items
     |                               |    ctx_summary_messages
     |  assemble (budget)            |    ctx_summary_parents
     v                               |
  +------------+                     |
  | Assembler  | <-------------------+
  +------------+
        |
        v
  []ai.Message (新鲜尾部 + token 预算内的摘要)
        |
        v
  LLM 上下文窗口
```

### Engine API

`Engine` 接口（`internal/memory/types.go`）是主要入口点：

| 方法                                          | 描述                                           |
| --------------------------------------------- | ---------------------------------------------- |
| `Bootstrap(ctx, sessionID)`                   | 确保会话的对话记录存在                         |
| `Ingest(ctx, sessionID, msg)`                 | 持久化单个 `ai.Message` 并添加上下文项         |
| `IngestBatch(ctx, sessionID, msgs)`           | 在单个事务中持久化多条消息                     |
| `Assemble(ctx, sessionID, budget, freshTail)` | 在 token 预算内构建上下文，返回 `[]ai.Message` |
| `Compact(ctx, sessionID, mode)`               | 运行压缩遍历（叶子 + 聚合）                    |
| `NeedsCompaction(ctx, sessionID, threshold)`  | 检查上下文 token 是否超过绝对阈值              |
| `Retrieval()`                                 | 返回用于搜索/探索工具的 `RetrievalEngine`      |
| `Close()`                                     | 释放数据库资源                                 |

**构造函数：** `NewEngineFromDB(db *sql.DB, summarizer Summarizer, opts ...EngineOption)` 接受现有的 `*sql.DB` 连接，以便内存引擎可以与配置存储和其他子系统使用的数据库句柄共享。

引擎选项：`WithFreshTail(n)`、`WithLogger(log)`。

### 数据库

- **位置：** `~/.anna/anna.db`
- **驱动：** `modernc.org/sqlite`（纯 Go，无 CGO）
- **模式：** WAL（写入期间并发读取），启用外键
- **迁移：** Atlas 生成的 SQL 文件在 `internal/db/migrations/` 中，通过 `MigrationsFS` 嵌入并在 `db.OpenDB()` 时应用。已应用版本在 `schema_migrations` 表中跟踪。

**模式更改工作流：**

```bash
# 1. 编辑模式源文件
vim internal/db/schemas/tables/conversations.sql

# 2. 生成迁移
mise run db:diff -- add_column_name

# 3. 重新生成 sqlc
mise run generate

# 4. 运行时在 OpenDB() 时自动应用待处理的迁移
```

**模式：**

| 表                        | 用途                                                                                                       |
| ------------------------- | ---------------------------------------------------------------------------------------------------------- |
| `ctx_conversations`       | 每个会话一条记录（`session_id` -> `id` 映射）。包含 `agent_id` 和 `user_id` 列以跟踪拥有对话的代理和用户。 |
| `ctx_messages`            | 原始消息，包含 `role`、`content`、`token_count`、顺序 `seq`                                                |
| `ctx_summaries`           | 摘要节点：`kind`（`leaf`/`condensed`）、`depth`、`content`、token 统计、时间范围                           |
| `ctx_items`               | 有序上下文窗口：每个项指向 `message_id` 或 `summary_id`                                                    |
| `ctx_summary_messages`    | 将叶子摘要链接到其源消息（保留血统）                                                                       |
| `ctx_summary_parents`     | 将聚合摘要链接到其父摘要（DAG 边）                                                                         |
| `ctx_message_parts`       | 结构化消息部分（`text`、`reasoning`、`tool`）供将来使用                                                    |
| `ctx_agent_memory`        | 每用户每代理笔记。主键是 `(user_id, agent_id)`。内容在会话开始时注入到系统提示中。                         |
| `settings_agents`         | 代理配置，包括 `system_prompt`（代理灵魂）、模型选择和工作区路径                                           |
| `settings_providers`      | LLM 提供商凭证和端点                                                                                       |
| `settings_channels`       | 通道（Telegram、QQ、Feishu）配置                                                                           |
| `settings_users`          | `ctx_agent_memory` 和 `ctx_conversations` 引用的用户记录                                                   |
| `settings_channel_agents` | 将通道映射到代理以进行多代理路由                                                                           |

### 压缩

压缩通过摘要旧消息和摘要来减少上下文窗口。

**模式：**

| 模式                    | 行为                                                        |
| ----------------------- | ----------------------------------------------------------- |
| `CompactionIncremental` | 单次叶子遍历 + 一次聚合遍历。当上下文超过阈值时自动运行。   |
| `CompactionFull`        | 重复叶子 + 聚合遍历，直到无法进一步压缩（最多 10 次迭代）。 |

**遍历：**

1. **叶子遍历** -- 查找新鲜尾部之外的连续消息上下文项运行。≥ `DefaultLeafChunkSize`（10）条消息的组被摘要成 `leaf` 摘要（深度 0）。消息上下文项被单个摘要上下文项替换。

2. **聚合遍历** -- 查找相同深度的连续摘要上下文项运行。≥ 2 个摘要的组被聚合成深度+1 的 `condensed` 摘要。使用来自预取的摘要缓存以避免冗余查询。

两个遍历都在 `runPasses` 辅助函数中运行，该函数获取上下文项一次，并仅在发生突变时在遍历之间重新获取。

**摘要升级**（`internal/memory/summarize.go`）：

`LLMSummarizer` 实现三层升级策略：

1. **正常模式** -- 保留关键决策、理由、约束、活动任务。目标：input_tokens/3。
2. **激进模式** -- 仅保留持久事实和当前任务状态。当正常模式超过目标的 150% 时触发。
3. **确定性回退** -- 在句子/行边界截断到目标。当激进模式仍然超过 150% 时触发。

叶子摘要目标为源 token 的 1/3。聚合摘要目标为 1/2（不太激进以保留细节）。

### 上下文组装

`Assembler` 为每次 LLM 调用构建上下文窗口（`internal/memory/assembler.go`）：

1. 将上下文项分为**新鲜尾部**（最后 N 个消息项，默认 20）和**较旧**项。
2. 将新鲜尾部项解析为 `ai.Message` -- 无论预算如何都始终包含这些。
3. 用较旧的项填充剩余预算，最新优先。每个项被解析并估计其 token。超出预算的项被排除。
4. 返回较旧事件（按时间顺序）+ 尾部事件。

**摘要 XML 格式**（作为合成用户消息注入）：

```xml
<summary id="sum_abc123" kind="leaf" depth="0" earliest_at="..." latest_at="...">
  <parents>
    <summary_ref id="sum_parent1" />
  </parents>
  <content>
    这里是摘要文本...
  </content>
</summary>
```

**Token 估计：** `(len(text) + 3) / 4`（每 token 约 4 个字符）。

### 内存工具

`internal/memory/tool/` 中的统一 `memory` 工具通过 `action` 参数提供所有内存操作：

| 操作                 | 目的                                                                                             | 关键参数                                                                        |
| -------------------- | ------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------- |
| `grep`               | 按子串模式搜索消息和摘要                                                                         | `pattern`（必需）、`scope`（`messages`/`summaries`/`both`）、`limit`（默认 20） |
| `describe`           | 检查摘要的元数据、内容和血统（父级/子级）                                                        | `summary_id`                                                                    |
| `expand`             | 深入摘要：返回源消息（叶子）或子摘要（聚合）                                                     | `summary_id`、`token_cap`（默认 4000）                                          |
| `user_memory_update` | 在 `ctx_agent_memory` 中只写更新每用户每代理持久化笔记。内容始终注入到系统提示中；此操作仅写入。 | `content`（必需）                                                               |

工具从上下文中提取会话 ID、用户 ID 和代理 ID（由 `Pool.Chat()` 设置）。

### 并发

- **每会话互斥锁** -- `Ingest`、`IngestBatch` 和 `Compact` 通过 `withSessionLock()` 获取每会话锁，以防止同一对话的并发突变。
- **全局互斥锁** -- 保护会话互斥锁映射和对话 ID 缓存。
- **对话 ID 缓存** -- `getOrCreateConversation` 缓存 `sessionID → convID` 映射，因为它一旦创建就是不可变的。

### 配置默认值

| 常量                      | 值   | 描述                     |
| ------------------------- | ---- | ------------------------ |
| `DefaultFreshTail`        | 20   | 受保护免于压缩的消息     |
| `DefaultContextThreshold` | 0.75 | 触发压缩的预算分数       |
| `DefaultLeafChunkSize`    | 10   | 每个叶子摘要的最小消息数 |

### 集成

内存引擎连接到代理 Pool。当会话使用它时：

1. 每条消息在每轮后被摄取到数据库中。
2. 在每次 LLM 调用之前从数据库组装上下文。
3. 压缩根据上下文阈值自动运行。

---

## 身份与用户记忆

代理身份和每用户记忆存储在数据库中，并可选通过工作区文件覆盖。

### 三层系统提示

发送到 LLM 的系统提示从三层组装，每层都可覆盖：

| 层           | 默认源                          | 文件覆盖                   | 描述                                           |
| ------------ | ------------------------------- | -------------------------- | ---------------------------------------------- |
| **基础**     | 内置系统指令                    | 代理工作区中的 `SYSTEM.md` | LLM 的核心行为指令。                           |
| **代理灵魂** | `settings_agents.system_prompt` | 代理工作区中的 `SOUL.md`   | 代理身份、个性和语气。                         |
| **用户记忆** | `ctx_agent_memory.content`      | （无 -- 始终来自数据库）   | 每用户每代理笔记。自动注入；不可通过文件覆盖。 |

层按顺序连接（基础，然后代理灵魂，然后用户记忆）以形成最终系统提示。

### 代理灵魂

代理灵魂定义个性和语气。它存储在 `settings_agents.system_prompt` 中，可以通过 admin 面板管理。如果代理工作区（`$ANNA_HOME/workspaces/{agent_id}/SOUL.md`）中存在 `SOUL.md` 文件，其内容优先于数据库值。

### 用户记忆

用户记忆存储在 `ctx_agent_memory` 表中，键为 `(user_id, agent_id)`。这允许每个用户为每个代理拥有不同的笔记。内容始终在会话开始时注入到系统提示中。

代理通过 `memory` 工具的 `user_memory_update` 操作更新用户记忆。此操作是**只写的** -- 它替换当前用户/代理对的 `ctx_agent_memory` 行的整个内容。代理不能通过工具读取用户记忆；它始终通过系统提示注入提供。

### 代理工作区

每个代理在 `$ANNA_HOME/workspaces/{agent_id}/` 都有自己的工作区目录。此目录包含文件覆盖（`SYSTEM.md`、`SOUL.md`）、技能和任何其他每代理数据。
