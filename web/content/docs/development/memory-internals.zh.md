---
title: 记忆系统内部实现
---

> 本节面向为 Stella 贡献代码的开发者。

## Provider 接口

核心 `Provider` 接口（`internal/memory/provider.go`）包含 5 个方法：

| 方法                                        | 说明                             |
| ------------------------------------------- | -------------------------------- |
| `Bootstrap(ctx, session)`                   | 确保会话的对话记录存在           |
| `Append(ctx, session, msgs)`                | 持久化消息并添加上下文项         |
| `Assemble(ctx, session, budget, freshTail)` | 在 token 预算内组装对话上下文    |
| `Stats(ctx, session)`                       | 返回会话统计（token 数、消息数） |
| `Close()`                                   | 释放资源                         |

### 可选能力

Provider 可以通过类型断言实现额外接口：

| 接口                                                 | 说明                                |
| ---------------------------------------------------- | ----------------------------------- |
| `Compactor`                                          | 上下文窗口压缩                      |
| `Searcher`                                           | 跨消息和摘要的全文搜索              |
| `Explorer`                                           | 检查和深入摘要                      |
| `ProfileStore`                                       | 每用户每代理的 profile 和 soul 文本 |
| `ConstraintStore`                                    | 每用户每代理的硬性约束              |
| `ChangelogReader` / `ChangelogWriter`                | 记忆写入版本历史                    |
| `VersionedProfileStore` / `VersionedConstraintStore` | 按冻结版本读取身份/约束状态         |
| `SessionSnapshotStore`                               | 冻结和推进每会话记忆版本            |
| `SessionManager`                                     | 会话元数据和历史管理                |
| `ReviewSource`                                       | Reflect 自我改进评审数据            |

LCM 插件实现完整能力。Simple 插件实现核心 Provider、身份、约束、changelog、snapshot 和会话管理，但不支持压缩、搜索和探索。

## 记忆工具

`memory.BuildTool(provider)` 会检查 provider 能力，并生成匹配动作的 `tools.Tool`：

| 动作                | 需要接口                           | 说明                                        |
| ------------------- | ---------------------------------- | ------------------------------------------- |
| `status`            | 始终可用                           | 显示会话统计                                |
| `search`            | `Searcher`                         | 按模式搜索消息和摘要                        |
| `describe`          | `Explorer`                         | 检查摘要的元数据和血统                      |
| `expand`            | `Explorer`                         | 深入压缩后的摘要                            |
| `profile_get`       | `ProfileStore`                     | 读取持久用户画像笔记                        |
| `profile_update`    | `ProfileStore`                     | 替换持久用户画像笔记                        |
| `soul_get`          | `ProfileStore`                     | 读取每用户 agent soul 覆盖                  |
| `soul_update`       | `ProfileStore`                     | 更新每用户 agent soul 覆盖                  |
| `profile_history`   | `ChangelogReader`                  | 查看最近 profile/soul 变更历史              |
| `profile_rollback`  | `ChangelogReader` + `ProfileStore` | 从 changelog 的旧版本恢复 profile/soul 文本 |
| `constraint_list`   | `ConstraintStore`                  | 列出硬性约束                                |
| `constraint_add`    | `ConstraintStore`                  | 在对话中获得用户确认后添加硬性约束          |
| `constraint_remove` | `ConstraintStore`                  | 按 ID 删除硬性约束                          |

工具的 JSON schema、描述和调度都会动态适配。能力较少的 provider 会生成动作较少的工具。

### 群聊回合:当前发言人回退

群 session 的运行时身份是群,因此没有 session 用户(D9)。为了仍能让 agent 记住正在说话的人的事实,当不存在 session 用户时,`profile_get` 与 `profile_update` 回退到当前发言人:

1. session 用户(`UserIDFromContext`)—— 正常 DM 行为。
2. 否则已关联的当前发言人(`CurrentSpeaker.UserID`)—— 群个性化。
3. 否则 fail-closed,报 `no linked current speaker`(未关联发送者)。

回退刻意收窄。**只有 `profile_get` / `profile_update` 获得它,且只有模型显式调用工具时才会发生。** `soul_get`、`soul_update`、`constraint_*`、`profile_history`、`profile_rollback` 仍走严格的 session-用户解析器,因此群聊回合中它们 fail-closed——公开群不是通过共享 agent 读取或改写某成员 soul、constraints、历史的地方。经回退的 `profile_update` 推进发言人自己的快照行 `(session, speaker.UserID, agent)`,绝不推进群的。

参见[群聊:当前发言人(D10)](/docs/development/group-chat-multi-agent#current-speaker-per-turn-personalization-d10)。

## 系统提示层级

每一轮对话都可以从当前或冻结的记忆版本重建系统提示。顺序如下：

1. **基础系统提示** —— agent 配置 / `SYSTEM.md` 覆盖。
2. **工具和插件提示清单** —— 可用工具、插件能力、技能。
3. **约束** —— 来自 `ConstraintStore` 的用户确认硬规则；位于 soul/profile 之前，Reflect 不会修改。
4. **Agent soul** —— agent 身份、人格和语气文本。
5. **用户画像** —— 持久用户笔记。**群聊回合用 `## Group Memory`(共享群抽屉)加可选的 `## Current Speaker` 段替换它**,该段只包含发言人姓名和关联状态;群聊回合绝不渲染按用户的画像。群模式按 session 是否有 `group_id` 分支,而非按群记忆是否为空。
6. **知识** —— 来自 `KnowledgeStore` 的 active fact/context 条目。
7. **项目上下文** —— `AGENTS.md` 等项目指令。

对话历史由记忆 provider 单独组装。约束、身份和知识位于系统提示中，因此对话压缩不会删除它们。

群聊回合由 PoolManager 的 before-run 路径带当前发言人元数据重渲整份提示词;缓存的群 runner 不持有发言人数据,故一个发言人的回合上下文不会泄漏到另一个发言人的回合。发言人的 profile 正文和带日期条目不会自动注入公开群 prompt;profile 访问仍必须通过显式的 `memory.profile_get` / `memory.profile_update` 工具调用。

## Changelog 与回滚

`ctx_agent_memory` 有行级 `version`。profile、soul、constraints 的写入会递增 version，并在同一个数据库事务中向 `memory_changelog` 追加记录。

changelog 记录：

- 用户和 agent
- scope（`profile`、`soul`、`constraint`、`skill`、`compaction`）
- action（`create`、`update`、`delete`、`compact`）
- source（`user`、`agent`、`reflect`、`system`）
- 写入前/后的文本
- 写入前/后的记忆版本
- 可选的 session/entity 元数据

这支持 `profile_history`、`profile_rollback`、审计，以及会话快照所需的按版本读取。

## 约束

约束以 JSON 数组形式存储在 `ctx_agent_memory.constraints`。每条包含 ID、文本和创建时间。

约束适合保存用户明确希望 Stella 长期遵守的规则，例如：

- "删除文件前先询问。"
- "未经我批准，不要运行生产数据库迁移。"
- "不要在聊天中暴露密钥。"

Reflect 被明确禁止添加、删除或编辑约束。当前保护是约定级：模型应该先用自然语言提出约束，只有用户同意后才调用 `constraint_add`。

## 会话快照

会话快照用于防止后台记忆更新在活跃对话中途改变行为。

第一次聊天时，Stella 会为 `(session_id, user_id, agent_id)` 在 `memory_snapshots` 中保存冻结的 `ctx_agent_memory.version`。每一轮对话前，`runtime.Runtime` 使用该快照版本重建系统提示，并通过 per-run system override 注入。

可见性规则：

| 写入路径                             | 当前会话是否可见？ | 原因                                              |
| ------------------------------------ | ------------------ | ------------------------------------------------- |
| 用户通过记忆工具要求 Stella 记住某事 | 是，从下一轮开始   | 记忆工具会推进当前会话快照                        |
| 用户通过记忆工具添加/删除约束        | 是，从下一轮开始   | 前台写入后推进 snapshot                           |
| Reflect 在后台更新 profile/knowledge | 否                 | Reflect 没有活跃 session context，不推进 snapshot |
| 新会话开始                           | 是                 | 新会话会快照最新记忆版本                          |

这样既保证用户前台意图能及时生效，又避免后台反思在进行中的会话里造成行为漂移。

## 知识

知识通过 `metadata.knowledge_type` 扩展 skills 表：

| 类型      | 含义                 | 模型可调用？ | 默认过期    |
| --------- | -------------------- | ------------ | ----------- |
| `skill`   | 可复用流程或操作步骤 | 是           | draft 30 天 |
| `fact`    | 持久项目/领域事实    | 否           | draft 90 天 |
| `context` | 有时效性的背景信息   | 否           | draft 30 天 |

Fact/context 条目存储在 `skills` 表中，并设置 `disable_model_invocation=true`。它们不会出现在 `<available_skills>` 中，也不能通过 skills tool 当作可执行技能加载。Active 条目会注入系统提示的 `## Knowledge` 区块。

Reflect 可以创建 fact/context 草稿，但草稿不会影响会话；需要通过 skills/admin 管理路径激活后才会进入系统提示。

## LCM 插件

### 架构

```
ai.Message (user/assistant/tool_result)
        |
        v
  +----------+     Append     +-----------+
  | Provider | ------------> | SQLite DB |
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
  +-----------+                      |
  | Assembler | <--------------------+
  +-----------+
        |
        v
  []ai.Message (fresh tail + summaries within token budget)
```

### 压缩

压缩通过摘要旧消息和摘要来减少对话窗口。如果 provider 没有 summarizer，压缩会被禁用，不再使用空摘要回退。

1. **叶子遍历** —— 将 fresh tail 之外的连续消息项分组。10 条以上的消息组会变成 `leaf` 摘要。
2. **聚合遍历** —— 将相同深度的摘要分组。2 个以上的摘要组会变成更高深度的 `condensed` 摘要。

在送入摘要模型之前，tool result 和 tool call 会先被格式化为紧凑、可读的形式，而不是原始 JSON：

| 事件类型              | 格式化输出示例                                            |
| --------------------- | --------------------------------------------------------- |
| `tool_result`         | `[tool:read_file] result(1234 chars): first 300 chars...` |
| `tool_result`（错误） | `[tool:read_file] error: file not found`                  |
| `tool_call`           | `[assistant:call bash] args: {"command":"ls"}`            |
| text / image / 其他   | `[user] hello`（保持原格式）                              |

如果消息无法解析（遗留数据或损坏的 JSON），格式化器会回退到原始的 `[role] content` 字符串。

摘要会按需从普通模式升级到"只保留持久事实"的激进模式，最后再退回到按句子边界截断的确定性模式。

### 上下文组装

1. 将上下文项分为 **fresh tail**（最后 N 个用户轮次，默认 6）和较旧项。一个轮次从用户消息开始，包含直到下一条用户消息之前的所有 item。
2. 如果按轮次得到的 tail 超过 120 个消息项，则回退为保留最后 120 个消息项，并继续修正 tool pair 边界。这保证单用户触发的长 agent 循环仍可被压缩。
3. 将 fresh tail 解析为 `ai.Message`。
4. 将 tail 中已处理完的大型 tool result（>2000 tokens）替换为紧凑占位符，同时保留 `ToolCallID`、`ToolName`、`IsError` 和 `Timestamp`，以便模型知道内容已被省略并在需要时重新调用工具。仍在处理中的 tool result（尚无 assistant 回复）保持完整大小。
5. 仅在组装阶段，将 fresh tail 限制在 token 预算的 40% 内；若超限，就把最旧的完整 tail 轮次移回较旧项参与预算竞争。至少保留 1 个轮次。该上限基于占位符压缩后的 tail 计算，超大 tool result 不会把自己所在的轮次挤出 tail。
6. 在已压缩的 tail 上计算 token 成本，然后用较旧项填充剩余预算，最新优先。
7. 返回按时间顺序排列的较旧事件，再接 fresh tail。

每次组装都会输出一条结构化日志（`lcm tail telemetry`），包含 `tail_items`、`tail_messages`、`user_turns`、`items_per_turn` 和 `tool_results_before/after`，用于数据驱动的参数调优。

### 搜索

搜索基于 PostgreSQL 的 **pg_search BM25** 排序。`ctx_message` 与 `ctx_summary` 各自在 `content` 上建有 `USING bm25` 索引，使用 **ICU** 分词器，因此中文会被切成词（部署方案 命中切分后的 部署 / 方案），英文按整 token 匹配。**没有回退层** — pg_search 硬依赖 `pg_search` 扩展（语义检索还需 `vector`），它们随内嵌运行时 bundle 或外部 PostgreSQL 一起提供。

- 原始用户文本直接交给 `paradedb.match`，它用 ICU 分词，且对标点或查询语法字符永不报错 — 因此既无需单独的清洗步骤，短查询和中文查询也能原生命中（没有最小 token 长度限制，没有 `LIKE` 回退）。
- 命中包含 pg_search 片段（`<b>term</b>` 高亮）和 `paradedb.score` BM25 分数（越大越相关）。
- `both` 范围分别以完整 limit 查询消息和摘要，再按分数合并取前 N — 强摘要命中可以排在弱消息命中之前。摘要命中可通过 `describe`/`expand` 下钻。
- BM25 索引位于 schema 基线（`internal/db/migrations`）；`vector`/`pg_search` 扩展在**运行时**（`internal/db/database.go` 的 `ensureExtensions`）于迁移前创建，因为 `CREATE EXTENSION` 需要二进制（以及 `shared_preload_libraries=pg_search`），迁移无法保证这些。
- 语义检索使用按来源拆分的 sidecar 表（`ctx_message_embedding`、`ctx_summary_embedding`、`recally_article_embedding`），各持有一列 `vector(1536)` 并建有 HNSW 索引，以来源 id 为键。它们初始为空：embedding 生产/回填在后续 PR 落地。

## Simple 插件

Simple 插件使用滑动窗口方式：

1. **Append** 将消息存储在 `ctx_messages`。
2. **Assemble** 返回最近 N 条符合 token 预算的消息，并始终保留 freshTail。
3. 无摘要、无压缩、无搜索、无探索。

适用于短会话或资源受限环境。

## 数据库

- **位置：** `~/.stella/stella.db`
- **驱动：** `modernc.org/sqlite`（纯 Go，无 CGO）
- **模式：** WAL，启用外键
- **迁移：** Atlas 生成，通过 `MigrationsFS` 嵌入，启动时自动应用。

**核心 schema：**

| 表                     | 用途                                                        |
| ---------------------- | ----------------------------------------------------------- |
| `ctx_conversations`    | 每会话一条（`session_id` -> `id` 映射），包含 agent/user ID |
| `ctx_messages`         | 原始消息，包含 `role`、`content`、`token_count`、顺序 `seq` |
| `ctx_summaries`        | 摘要 DAG 节点                                               |
| `ctx_items`            | 有序上下文窗口：指向消息或摘要                              |
| `ctx_summary_messages` | 将叶子摘要链接到源消息                                      |
| `ctx_summary_parents`  | 将聚合摘要链接到父摘要                                      |
| `ctx_agent_memory`     | Profile、soul、constraints 和行级 version                   |
| `memory_changelog`     | 记忆写入的追加式审计日志                                    |
| `memory_snapshots`     | 每会话冻结的记忆版本                                        |
| `skills`               | 技能，以及不可调用的 fact/context 知识条目                  |

## 配置默认值

| 常量                         | 值    | 说明                                           |
| ---------------------------- | ----- | ---------------------------------------------- |
| `DefaultFreshTail`           | 6     | 受保护免于压缩的用户轮次                       |
| `CompactionConfig.MaxTokens` | 80000 | 触发压缩的绝对上下文 token 数                  |
| `DefaultLeafChunkSize`       | 10    | 每个叶子摘要的最小消息数                       |
| `OversizedToolResultTokens`  | 2000  | 超过此阈值的 tail tool result 会被替换为占位符 |

## Agent 工作区

每个 agent 在 `$STELLA_HOME/agents/{agent_id}/` 有自己的工作区，用于文件覆盖、技能和每 agent 数据。
