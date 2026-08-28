---
title: Agent Session 取证分析
---

> 本文面向排查 Agent turn 变慢、成本过高、结果错误或工具调用冗余的开发者。它说明如何从一条持久化 session 推导假设，不是性能基准方法。

Stella 会把一条 session 留下两类互补的 PostgreSQL 记录：

- `ctx_conversation` 与 `ctx_message` 保存面向用户的转录，包括持久化的工具调用与结果。
- `agent_llm_call` 保存每次模型调用的 provider 上报 token、延迟、模型、停止原因与错误。

二者合起来回答 Agent 做了什么、给模型看了什么、turn 花在哪里。它们不能证明某个修改提升了 Agent 行为。要提出这种结论，必须把诊断转成确定性 replay 或配对的 Harbor 对比。

## 先处理数据安全

Session 转录可能包含用户内容、URL、工具漏脱敏的凭据和私有业务数据。按生产客户数据处理。

- 只能查询获授权的部署和用户范围。
- 使用只读数据库角色。排查期间绝不执行 `UPDATE`、`DELETE`、迁移或临时清理。
- 原始转录只留在受保护的机器上。粘贴到 issue、PR、日志或模型提示前，先脱敏片段。
- 不要为了复现 session 而在 Harbor 中开启 `OTEL_STELLA_RECORD_TOOL_IO`。Terminal-Bench 任务包含合成 secret。

让部署的 secret manager 或受保护的终端环境提供 `STELLA_DATABASE_URL`，不要把包含密码的连接串直接输入 shell history。然后连接：

```sh
psql "$STELLA_DATABASE_URL"
```

下列示例将 session ID 作为 SQL 参数。在 `psql` 中设置一次，不要把它直接插值进查询：

```sql
\set session_id '00000000-0000-0000-0000-000000000000'
```

## 先确定 session 边界

从 `ctx_conversation` 开始。Session ID 是文本标识符，conversation 行的 UUID 才是 `ctx_message` 使用的外键。

```sql
SELECT
  id,
  session_id,
  title,
  channel,
  kind,
  agent_id,
  user_id,
  created_at,
  last_active,
  archived
FROM ctx_conversation
WHERE session_id = :'session_id';
```

读取内容前确认 `agent_id`、`user_id`、channel 和时间范围。Session 可能是私聊、群聊、调度任务、delegate 或 task，不同 kind 的预期工具面和 turn 策略不同。

## 还原工具时间线

按 `seq` 读取消息。`tool_call` 和 `tool_result` 行将 payload 作为 `content` 中的 JSON 文本存储，其中的 call ID 关联一对调用和结果。先取受限且本地脱敏的片段，不要直接导出整段转录。

```sql
SELECT
  m.seq,
  m.created_at,
  m.role,
  m.event_type,
  m.token_count,
  left(m.content, 1000) AS redacted_locally_before_sharing
FROM ctx_message AS m
JOIN ctx_conversation AS c ON c.id = m.conversation_id
WHERE c.session_id = :'session_id'
ORDER BY m.seq;
```

在提出修改前，先给序列分类：

| 转录中的证据                             | 可能问题                         | 优先考虑的修改                                                |
| ---------------------------------------- | -------------------------------- | ------------------------------------------------------------- |
| 简单请求在真正执行前加载了多份 reference | Happy path 藏在可选说明之后      | 将短默认路径放进 `SKILL.md`，只在明确高级意图时加载 reference |
| 失败命令之后是近似相同的重试             | 缺依赖或 fallback 不够不同       | 移除脆弱依赖，或让下一条 fallback 真正不同                    |
| 同一文章或文档反复出现在工具结果中       | 大型中间内容回流到模型           | 保留在 sandbox 文件中，通过路径或引用在工具间传递             |
| 工具调用先校验失败、重试后成功           | 提示说明与生成的工具 schema 漂移 | 写出精确请求形状，并加读取 schema 的契约测试                  |
| 内容已抓取成功，却再次抓取 metadata      | Agent 丢掉了可复用的中间数据     | 产出紧凑 sidecar 结果，并在下一调用中复用                     |

一条转录是一次执行的证据，不是普遍失败模式。修改前必须从当前 skill、prompt、工具 schema 和 sandbox 实现确认该路径确实可达。

## 核算模型调用与成本

`agent_llm_call` 有 `session_id`，但没有指向具体转录消息的外键。按 `occurred_at` 与转录对齐，不要假设一条消息或一次工具调用必然对应一次模型调用。

```sql
SELECT
  occurred_at,
  provider,
  model,
  input_tokens,
  output_tokens,
  cache_read_tokens,
  cache_write_tokens,
  duration_ms,
  time_to_first_token_ms,
  stop_reason,
  error
FROM agent_llm_call
WHERE session_id = :'session_id'
ORDER BY occurred_at;
```

对正在研究的保存或任务窗口，使用聚合建立紧凑 baseline：

```sql
SELECT
  count(*) AS model_calls,
  sum(input_tokens) AS input_tokens,
  sum(output_tokens) AS output_tokens,
  sum(cache_read_tokens) AS cache_read_tokens,
  sum(duration_ms) AS model_duration_ms,
  count(*) FILTER (WHERE error <> '') AS errored_calls
FROM agent_llm_call
WHERE session_id = :'session_id';
```

Wall-clock 时长适合定位停顿，但不能跨主机、模型、缓存状态或网络条件比较。形成假设时，优先使用模型 turn、工具调用、工具错误和 provider 上报 token 的计数。

## 将 turn 证据变成安全改进

完成取证后按此流程推进：

1. **精确陈述观察到的浪费。** 例如：“一次裸 URL 保存加载了两份 reference，进行了三次抓取，消耗八个模型 turn。” 描述中不要保留原文文章或标识符。
2. **写出不变量。** 例如：保存的 body 不得进入模型上下文；新的 Recally 保存必须使用 `articles` batch；metadata 必须有类型且受限。
3. **停在能执行该不变量的最小层。** 优先使用既有 skill 指令、生成的工具契约、sandbox 文件传输或单元测试。不要在 sandbox 已拥有网页抓取及其出网策略时新增服务端抓取 endpoint。
4. **增加确定性守卫。** 可在 sandbox 执行的 prompt recipe 需要带 stub 依赖的执行测试。文档化的工具请求需要检查其字段与生成 input schema 一致的测试。
5. **重放窄行为。** 使用 fixture、stub 上游响应和新 session。验证保存数据，及 body 或 secret 没有进入工具参数、Code source 或持久化模型历史。
6. **在正确层级衡量 Agent 行为。** Session 取证解释为什么修改。只有 Harbor 的 task 与启用工具面真的覆盖该行为时，Harbor 才提供配对前后证据。当前受信任 Harbor loop 刻意只允许 bash，因此不能验证 Skill/Tap/Recally 流程。应先建立确定性 specialized-tool replay，而不是宣称无关的 Harbor 分数。

## 可供评审的总结

有用的排查总结应将事实与解释分开：

```text
观察到的 session 窗口：<UTC start> 到 <UTC end>
结果：<completed / failed / partial>
证据：<turns, tool calls, failed calls, provider tokens>
浪费：<具体重复工作或无效调用>
根因：<已验证的 prompt、schema 或 sandbox 路径>
修改：<选择的最小层与不变量>
验证：<fixture/unit/system test>
评测边界：<Harbor 或 replay 覆盖什么，以及没有覆盖什么>
```

这样既能让 session 排查可复现，又无需导出私有转录，并防止一个有说服力的个案被误当成无证据的性能结论。
