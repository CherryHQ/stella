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
| `InboxAppender`                                      | 原子认领 Session inbox 行并追加消息 |
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

`memory.NewRecall(provider, sessionAccess, groupRecall)` 只检查一次 provider 能力，`memory.NewTool` 把每份生成的声明绑到这个共享 surface 上。模型看到的正好是两个工具：

### 普通 Agent surface

| 工具            | 说明                                                                                                                                            |
| --------------- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| `memory_search` | 搜索当前快照可见的全部记忆：对话消息与摘要、持久 facts、profile、soul 和 constraints。结果携带 opaque ref。                                     |
| `memory_read`   | 解析搜索结果 ref，或读取 well-known ref：`profile`、`soul`、`constraints`、`profile_versions`、`soul_versions`。摘要可通过 child ref 继续下钻。 |

因此 Agent 只需理解一个回忆流程：先搜索记忆，再读取感兴趣的结果；不必在消息、摘要、知识或身份搜索 API 之间做选择。Session 管理保持独立：`session_list` 列出最近、活跃或已归档 Session；`session_get` 检查已知 Session 并对其有界 transcript 分页；`session_create` 和 `session_send` 管理工作。

这个 façade 不暴露 LCM 存储模型，同时保留其检索能力：

| LCM 能力                        | 普通 Agent 投影                                                                      |
| ------------------------------- | ------------------------------------------------------------------------------------ |
| `Searcher.Search`               | `memory_search` 将消息和摘要命中与持久记忆命中统一排序返回。                         |
| `MessageReader.GetMessage`      | `memory_read` 根据 opaque search ref 返回完整消息。                                  |
| `Explorer.Describe` 与 `Expand` | `memory_read` 返回摘要 metadata、lineage 和一层有界 children；可继续读取 child ref。 |

Opaque ref 只是 locator，不是 capability token。每次读取 dynamic ref 都会重新经过 Session policy enforcement point，校验当前 user、agent、Session、conversation 和 snapshot。返回 summary descendants 之前，也会逐个校验它们属于同一 conversation。搜索与展开受结果数、文本大小、token 预算和序列化输出限制；聚合读取省略内容或 ref 时会设置 `truncated`。搜索只返回 top window，不提供 cursor，因为底层 LCM 排序并不支持稳定分页。

### 内部管理 surface

退役 union 背后的 provider 能力依然存在，消失的是模型调用它们的入口。会话统计、摘要
describe/expand、整条消息读取、持久 profile 与 soul 写入、版本历史与回滚、约束写入，
现在由 HTTP 管理端点、Reflect，以及各自负责授权的 manual UI/API/CLI surface 承担。
要重新对模型开放其中一项，就在 `api/spec/agent-tools/memory.yaml` 里为它声明一个带
独立 sealed schema 的工具（例如 `memory_profile_update`），而不是把 union action 恢复回来。

### 群聊回合:当前发言人回退

群 session 的运行时身份是群,因此没有 session 用户(D9)。为了仍能让 agent 读取正在说话的人的事实,当不存在 session 用户时，well-known ref `profile` 的 `memory_read` 会回退到当前发言人。同一个 resolver 也支撑管理 surface 的 profile 写入，所以它解析的是写入目标而不只是读取目标:

1. session 用户(`UserIDFromContext`)—— 正常 DM 行为。
2. 否则已关联的当前发言人(`CurrentSpeaker.UserID`)—— 群个性化。
3. 否则 fail-closed,报 `no linked current speaker`(未关联发送者)。

回退刻意收窄。**普通聊天中只有显式读取 `profile` 的 `memory_read` 能获得它。**统一搜索以及 `soul`、`constraints`、版本历史或 dynamic transcript ref 的读取仍走严格的 session-用户解析器，因此群聊回合中会 fail-closed——公开群不是通过共享 agent 搜索或读取某成员私有记忆的地方。管理 surface 经回退写入 profile 时，只推进发言人自己的快照行 `(session, speaker.UserID, agent)`,绝不推进群的。

参见[群聊:当前发言人(D10)](/docs/development/group-chat-multi-agent#current-speaker-per-turn-personalization-d10)。

## 系统提示层级

每一轮对话都可以从当前或冻结的记忆版本重建系统提示。顺序如下：

1. **基础系统提示** —— agent 配置 / `SYSTEM.md` 覆盖。
2. **工具和插件提示清单** —— 可用工具、插件能力、技能。
3. **约束** —— 来自 `ConstraintStore` 的用户确认硬规则；位于 soul/profile 之前，Reflect 不会修改。
4. **Agent soul** —— agent 身份、人格和语气文本。
5. **用户画像** —— 持久用户笔记。群聊回合绝不渲染按用户的画像。群模式按 session 是否有 `group_id` 分支。
6. **知识** —— facts 表里的 active `subject=world` 事实。
7. **项目上下文** —— `AGENTS.md` 等项目指令。

对话历史由记忆 provider 单独组装。约束、身份和知识位于系统提示中，因此对话压缩不会删除它们。

群聊回合由 PoolManager 的 before-run 路径带当前发言人元数据重渲整份提示词;缓存的群 runner 不持有发言人数据,故一个发言人的回合上下文不会泄漏到另一个发言人的回合。发言人的 profile 正文和带日期条目不会自动注入公开群 prompt;profile 访问仍必须通过显式的只读 `memory_read` 读取 `profile`。

## Changelog 与回滚

`ctx_agent_memory` 有行级 `version`。profile、soul、constraints 的写入会递增 version，并在同一个数据库事务中向 `memory_changelog` 追加记录。

changelog 记录：

- 用户和 agent
- scope（`profile`、`soul`、`constraint`、`skill`、`compaction`）
- action（`create`、`update`、`delete`、`deprecate`、`compact`）
- source（`user`、`agent`、`reflect`、`system`）
- 写入前/后的文本
- 写入前/后的记忆版本
- 可选的 session/entity 元数据

这支持 profile/soul 历史与回滚管理端点、审计，以及会话快照所需的按版本读取。

## 约束

约束以 JSON 数组形式存储在 `ctx_agent_memory.constraints`。每条包含 ID、文本和创建时间。

约束适合保存用户明确希望 Stella 长期遵守的规则，例如：

- "删除文件前先询问。"
- "未经我批准，不要运行生产数据库迁移。"
- "不要在聊天中暴露密钥。"

Reflect 被明确禁止添加、删除或编辑约束。普通会话工具也不暴露 constraint 写入动作；constraints 只通过 UI/API/CLI 等 manual 入口，在用户明确意图下写入。

## 会话快照

会话快照用于防止后台记忆更新在活跃对话中途改变行为。

第一次聊天时，Stella 会为 `(session_id, user_id, agent_id)` 在 `memory_snapshots` 中保存冻结的 `ctx_agent_memory.version`。每一轮对话前，`runtime.Runtime` 使用该快照版本重建系统提示，并通过 per-run system override 注入。

可见性规则：

| 写入路径                            | 当前会话是否可见？ | 原因                                              |
| ----------------------------------- | ------------------ | ------------------------------------------------- |
| UI/API/CLI 手动更新 profile/soul    | 新会话可见         | manual 写入更新 durable facts；当前会话保持快照   |
| UI/API/CLI 手动添加/删除 constraint | 新会话可见         | manual 写入更新 constraints；当前会话保持快照     |
| Reflect 在后台更新 profile          | 否                 | Reflect 没有活跃 session context，不推进 snapshot |
| 新会话开始                          | 是                 | 新会话会快照最新记忆版本                          |

这样可以避免普通会话工具写入和后台反思在进行中的会话里造成行为漂移。

## 知识

知识在 v1 中存储为 facts 表里的 active `subject=world`、`scope=user_agent` 记录。prompt renderer 会把这些 facts 投影到 `## Knowledge` 区块。

Skills 仍然只表示可复用流程，不再通过 `metadata.knowledge_type` 创建或存储 fact/context knowledge。旧的 `user_agent` skill-backed knowledge 会由 v1 facts migration 迁移为 `subject=world` facts；更宽的 knowledge scope 留给后续设计。

普通会话工具不直接写 facts。Structured Reflect 会生成并评估 Fact/Skill 候选、发现相关的 Reflect-owned 记录、协调通过门控的候选，并通过 host 校验后的操作分别写入两条线。Usage tracking 和 curator 负责维护 active Reflect-owned Knowledge/Skill 的生命周期。

## Structured Reflect 与 Curator

Structured Reflect 是 scheduler 唯一执行的 writer。Fact/Skill 两条线并发运行，但错误和 watermark 相互独立；一条线失败不会取消另一条，也不会推进失败线。

切换迁移会把每个 session 的旧 `review_watermark` 复制到缺失的 `reflect_watermark:fact` 和 `reflect_watermark:skill` 状态。line 已存在时保留时间更新的一方；如果旧 global 时间更新，则清除原 line sequence，因为该 sequence 属于更早的边界。迁移可以重复执行，并把 global row 原样保留为不再参与运行的回滚数据。运行时代码只读取和推进两条 line watermark。

Curator 仍使用独立的启动时配置：

| 环境变量                      | 可选值            | 默认值  | 含义                                             |
| ----------------------------- | ----------------- | ------- | ------------------------------------------------ |
| `STELLA_REFLECT_CURATOR_MODE` | `armed`、`shadow` | `armed` | 执行生命周期写入，或保持不产生写入的紧急停止状态 |

`armed` 模式缺少任何 Structured Reflect 或 curator 读写依赖时都会在启动阶段 fail closed。符合条件的 Reflect-owned Knowledge 会被废弃，并可通过经过身份认证的管理 API 恢复；符合条件的 Reflect-owned Skill 会被永久删除。

Curator Shadow 会执行与 armed 相同的确定性 eligibility scan，记录候选类型、record ID、命中规则、活动输入、候选数量、规则分布、耗时和错误，但不修改记录 status、changelog 或 usage state。它既是停止后续生命周期写入的紧急开关，也是观察生产扫描规模和 wiring 的只读模式。Ownership/scope gate、usage 检查、写入时 recheck 和依赖缺失时 fail-closed 由自动化测试保证。

部署后需要验证一次完整 Structured Reflect 运行、armed curator 的 eligible 写入、Knowledge 恢复，以及切回不产生写入的 Shadow。回滚整个版本需要部署上一版本二进制；保留的 global 和 line watermark 状态可以让上一版本保守地继续处理。

## LCM 插件

### 架构

```
ai.Message (user/assistant/tool_result)
        |
        v
  +----------+     Append     +-----------+
  | Provider | ------------> | Postgres  |
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

搜索基于 PostgreSQL 的 **pg_search BM25** 排序。`ctx_message` 与 `ctx_summary` 各自在 `content` 上建有 `USING bm25` 索引，使用 **ICU** 分词器，因此中文会被切成词（部署方案 命中切分后的 部署 / 方案），英文按整 token 匹配。**没有回退层** — pg_search 硬依赖 `pg_search` 扩展（语义检索还需 `vector`），它们随下载的 runtime 或外部 PostgreSQL 一起提供。

- 原始用户文本直接交给 `paradedb.match`，它用 ICU 分词，且对标点或查询语法字符永不报错 — 因此既无需单独的清洗步骤，短查询和中文查询也能原生命中（没有最小 token 长度限制，没有 `LIKE` 回退）。
- 命中包含 pg_search 片段（`<b>term</b>` 高亮）和 `paradedb.score` BM25 分数（越大越相关）。
- `both` 范围分别以完整 limit 查询消息和摘要，再按分数合并取前 N — 强摘要命中可以排在弱消息命中之前。内部调用方通过 `Explorer.Describe`/`Expand` 下钻摘要；普通 Agent façade 将其投影为 `memory_read` 和有界 child refs。
- BM25 索引位于 schema 基线（`internal/db/migrations`）；`vector`/`pg_search` 扩展在**运行时**（`internal/db/database.go` 的 `ensureExtensions`）于迁移前创建，因为 `CREATE EXTENSION` 需要二进制（以及 `shared_preload_libraries=pg_search`），迁移无法保证这些。
- 语义检索使用按来源拆分的 sidecar 表（`ctx_message_embedding`、`ctx_summary_embedding`、`recally_article_embedding`），各持有一列 `vector(1536)` 并建有 HNSW（`vector_cosine_ops`）索引，以来源 id 为键。该通道**默认关闭、运行时配置**——没有任何 embedding 环境变量。管理员在 **管理 → 模型** 页面完成全部配置：嵌入模型（存入 `app_setting` 的 `default_models` 键下的 `model_embedding`），API 密钥与 Base URL 随之取自该模型所属的提供商；通道自身的开关、维度与是否归一化也在同一页面，仍保存在各自的 `embedding` 键中。修改即时生效，无需重启。
- 启用后，基于 River 的 worker 会为新内容生成向量并回填存量行；关闭时 worker 空转，检索回退为纯 BM25。每一行在其 `model` 列记录一个**空间键**（`provider/model@dim`），查询按 `WHERE model = $space` 过滤——因此用不同提供商/模型/维度生成的查询向量只会返回空结果而非错配结果；三者中任意一个变化都会重新嵌入到全新的向量空间。提供商之所以是空间键的一部分：同名模型由两个账号或两个端点提供时是两套不同的向量，跨着比对只会得到看似笃定的错误答案。
- 当两个通道都有命中时，`retrieval.go` 会分别对每个通道的分数做 min-max 归一化，再以 50/50 权重融合，并按 `source_type/source_id` 合并两路结果。

## Simple 插件

Simple 插件使用滑动窗口方式：

1. **Append** 将消息存储在 `ctx_messages`。
2. **Assemble** 返回最近 N 条符合 token 预算的消息，并始终保留 freshTail。
3. 无摘要、无压缩、无搜索、无探索。

适用于短会话或资源受限环境。

## 数据库

- **位置：** PostgreSQL —— 默认是 `~/.stella/postgres` 下的内嵌集群，或通过 `STELLA_DATABASE_URL` 指向外部服务器
- **驱动：** `pgx/v5`
- **迁移：** goose，嵌入并在启动时自动应用。

**核心 schema：**

| 表                     | 用途                                                                              |
| ---------------------- | --------------------------------------------------------------------------------- |
| `ctx_conversations`    | 每会话一条（`session_id` -> `id` 映射），包含 agent/user ID                       |
| `ctx_messages`         | 原始消息，包含 `role`、`content`、`token_count`、顺序 `seq` 和可选唯一 inbox 关联 |
| `ctx_session_inbox`    | 持久化 Agent 发起的 Session 输入；投递终态与 transcript 追加在同一事务中认领      |
| `ctx_summaries`        | 摘要 DAG 节点                                                                     |
| `ctx_items`            | 有序上下文窗口：指向消息或摘要                                                    |
| `ctx_summary_messages` | 将叶子摘要链接到源消息                                                            |
| `ctx_summary_parents`  | 将聚合摘要容器链接到组成它的子摘要（`parent_summary_id` 是历史遗留命名）          |
| `ctx_agent_memory`     | Profile、soul、constraints 和行级 version                                         |
| `memory_changelog`     | 记忆写入的追加式审计日志                                                          |
| `memory_snapshots`     | 每会话冻结的记忆版本                                                              |
| `skills`               | 技能，以及不可调用的 fact/context 知识条目                                        |

## 配置默认值

| 常量                         | 值    | 说明                                           |
| ---------------------------- | ----- | ---------------------------------------------- |
| `DefaultFreshTail`           | 6     | 受保护免于压缩的用户轮次                       |
| `CompactionConfig.MaxTokens` | 80000 | 触发压缩的绝对上下文 token 数                  |
| `DefaultLeafChunkSize`       | 10    | 每个叶子摘要的最小消息数                       |
| `OversizedToolResultTokens`  | 2000  | 超过此阈值的 tail tool result 会被替换为占位符 |

## Agent 工作区

每个 agent 在 `$STELLA_HOME/agents/{agent_id}/` 有自己的工作区，用于文件覆盖、技能和每 agent 数据。
