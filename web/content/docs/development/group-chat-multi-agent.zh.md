---
title: 群聊多 Agent
---

> 本页面面向开发 Stella 群聊支持的开发者:channel 适配器、消息 event log、arbiter/dispatcher、群记忆、session 身份。面向用户的指南见 channel 文档。

Stella 的群聊**让多个 agent 进入同一个物理群**。每个 agent 是独立的平台 bot;单个后端进程托管全部 bot,因此中央 arbiter 能成为真正可执行的发言闸门。本页记录让这件事安全的数据模型与身份规则。Web UI 与平台适配器共用的目标请求到回复流程见[群聊数据流](/docs/development/group-chat-dataflow)。

设计在动手前一次定死,因为大部分一旦带数据就难回头:新表(event log、群记忆、membership、ingest cursor)和 `ctx_conversation` 的归属列都是「上线带数据就回不去」的难改门。

## 统领一切的一条规则

一个群有**三个互不相等、谁也不许借谁名义的身份维度**:

| 维度                           | 取值                                                      | 用途                                        | 绝不用于                               |
| ------------------------------ | --------------------------------------------------------- | ------------------------------------------- | -------------------------------------- |
| **session scope**              | `group_id`(`ctx_group_state` 注册表的代理 id)             | LCM 查找键、conversation 历史、群记忆抽屉键 | 运行时身份(vault/token/workspace)      |
| **runtime execution identity** | agent 自己的群 principal `group:{group_id}`(非任何 human) | 工具执行、vault、workspace 路径             | 冒充任何成员;读任何 human 的私有 vault |
| **per-turn actor**             | 真实发言 human 的 `auth_user`                             | @寻址、写发言人**自己**的私有记忆、访问控制 | session 查找键、运行时执行身份         |

只需记住一件事:**群 session 绝不碰任何成员的私有资源。** 发言人的 `user_id` 按轮携带,用于寻址和访问控制,判完即弃——它绝不进入 workspace 路径、vault 或任何 agent 工具执行身份。

## Canonical 群身份(D0)

一个物理群聊在 `ctx_group_state` 注册一次,铸出一个**代理 `id`**(app uuid/ulid)作主键。所有群作用域表都引用这个 `id`——绝不重新拼字符串。映射到某个 `id` 的物理身份是三元组 `(platform, platform_group_id, platform_thread_id)`,由 `UNIQUE` 索引保证;任何 bot 观察到同一物理群/线程时,对三元组做 get-or-create,落到同一个 `id`。

为什么用代理 id 而非拼出来的 `platform:chat_id` 串?代理 id 不透明且稳定,扛得住平台侧 id 改格式,FK join 也便宜;三元组留作查找用的自然键。在每 agent 一个 bot 的架构下,每个 agent 是独立 bot = 独立 `channel_id`,所以**同一个物理群被 N 个 channel_id 观察到**。平台的群 id 是群全局的(不随哪个 bot 收到而变),所以三元组才是稳定群身份;`channel_id` 只回答「哪个 bot 看到的」。

**线程是独立的群。** Telegram 论坛话题(或任意平台子线程)是各自独立的会话,所以每个不同 `platform_thread_id` 拿到自己的注册行——自己的 event log、`seq`、记忆抽屉、arbiter 作用域。`platform_thread_id` 是 `TEXT NOT NULL DEFAULT ''`(空串,非 `NULL`):PostgreSQL 唯一索引默认把 `NULL` 视作互不相等,可空列会破坏三元组 `UNIQUE`。

`source_channel_id`(哪个 bot 观察到入站消息)记录在 event log 行上**仅供审计**。它绝不进入幂等键、`seq`、cursor 或 membership 主键,**也绝不作回复出口**。回复出口永远是发言 agent 自己的 `reply_channel_id`(见 membership)。

所有模块复用注册表 `id`——event log、群记忆、membership、ingest cursor、session key。任何模块不得自造「群」。

## agent 如何进群(D1)

群里每个 agent 都是一个绑定自己 channel 配置(自带 token)的独立 bot。平台负责身份、@mention、投递。单后端进程托管所有 channel,这正是 arbiter 能成为真闸门而非软建议的原因。

消息接入拓扑:

- **绑定 bot 必须能读群全量消息。** 进群前置:关闭平台隐私模式(Telegram BotFather `/setprivacy` → Disable;Feishu/QQ 对应授权范围)。读不到全量,arbiter 无从判断。
- **同一条人类消息可能被多个 bot 投递。** event log 的幂等键无论哪个 bot 送达都收敛成一行。
- **dispatch/arbiter 只触发一次**,仅在该消息首次成功插入 event log 时。

否掉的方案:一个 bot 扮演多个虚拟 agent。平台无法区分虚拟身份,@mention 和投递都要 Stella 自己模拟,arbiter 退化成建议。

## Event log(D2)

`ctx_group_message` 是每条群消息的权威、去重副本。关键列:

| 列                         | 说明                                                              |
| -------------------------- | ----------------------------------------------------------------- |
| `id TEXT PRIMARY KEY`      | app 生成 uuid/ulid(schema 规则要求 TEXT 主键)                     |
| `group_id TEXT NOT NULL`   | FK → `ctx_group_state(id)`(D0);所有去重/排序键按它                |
| `seq INTEGER NOT NULL`     | 群内单调 ordering token,从 1 起;`UNIQUE(group_id, seq)`           |
| `source_channel_id TEXT`   | 观察 bot——**仅审计**,不进任何唯一键                               |
| `actor_type TEXT NOT NULL` | `human` / `agent`——schema 级,绝不靠 content 猜                    |
| `actor_id TEXT NOT NULL`   | human → 平台 sender_id;agent → agent_id(不再单设 source_agent_id) |
| `platform_message_id TEXT` | 平台 id,可空(部分适配器给不出)                                    |
| `reply_to TEXT`            | 本条回复的平台消息 id;无则空/NULL                                 |
| `platform_timestamp TEXT`  | 平台上报发送时间(UTC);喂高精度去重兜底                            |
| `idempotency_key TEXT`     | 兜底去重键,仅在无稳定 `platform_message_id` 时设置                |
| `content TEXT NOT NULL`    | JSON 序列化的 `[]ai.ContentBlock`                                 |

### 去重:「宁可重复,不可静默丢」

三档,按优先级:

1. 有稳定 `platform_message_id` → 走 partial unique `(group_id, platform_message_id)` 去重,不生成幂等键。
2. 无稳定 id 但有**高精度平台时间** → `idempotency_key = hash(group_id, actor_id, platform_timestamp, content)`,partial unique(仅非空)。
3. 两者皆无 → **不生成幂等键**——该消息不可幂等但绝不被吞(接受偶发重复)。

绝不用本地接收时间或低精度/缺省时间凑 hash:那会把「连发两条相同内容」误判为重投而静默丢数据。

### seq 分配

全局序列给不了**按群**单调的 `seq`,app 层 `max+1` 并发会撞。改由注册行兼任 per-group 计数器与写锁:

```sql
CREATE TABLE ctx_group_state (
  id                 UUID PRIMARY KEY DEFAULT uuidv7(),  -- 群代理 id(D0)
  platform           TEXT NOT NULL,              -- 'telegram' | 'feishu' | 'qq' | ...
  platform_group_id  TEXT NOT NULL,              -- 平台原生群/chat id
  platform_thread_id TEXT NOT NULL DEFAULT '',   -- 子线程/话题;无则 ''
  next_seq           BIGINT NOT NULL DEFAULT 0,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (platform, platform_group_id, platform_thread_id)
);
-- 分配:UPDATE ... SET next_seq = next_seq + 1 WHERE id = $1 RETURNING next_seq(post-update 值;首条 = 1)
```

### 唯一写入路径

所有 append 走一个原语——**禁止裸 `INSERT`**:

```
AppendGroupMessage(ctx, msg) -> (result{inserted|existing}, seq)
```

闭合、幂等的算法(事务对注册行加 per-group 行锁——`SELECT ... FOR UPDATE`,或下面的原子 `UPDATE ... RETURNING`——使同一群的写入串行化):

0. **按三元组 `(platform, platform_group_id, platform_thread_id)` get-or-create 注册行** → 拿到代理 `id`(即 `group_id`)。
1. **锁内按 unique key 查重**(`platform_message_id` 或 fallback `idempotency_key`)。
2. **已存在** → 返回「不插入/不 bump/不 dispatch」。
3. **不存在** → `UPDATE ctx_group_state SET next_seq = next_seq + 1 WHERE id = $1 ... RETURNING next_seq` → `INSERT` 消息行(带该 seq)→ commit 后才 dispatch。

由于 bump 与 insert 都只在查重未命中分支、且在同一写锁内,幂等重投既不插行也不消耗 seq。`ON CONFLICT DO NOTHING` 仅作最后兜底,不承担判定职责。

## IncomingMessage 补字段(D3)

`pkg/channel.IncomingMessage` 加 `ThreadID / MessageID / Timestamp / ReplyTo / Mentions`。`ThreadID` 是 `ChatID` 内的平台子线程/话题 id(如 Telegram 论坛话题),喂 D0 注册三元组,使线程成为独立的群。`Mentions` 是 normalized 结构,不是平台原始串:

```go
type Mention struct {
    Raw        string // 平台原始 @ 文本(@username / <at open_id> ...),审计/兜底
    PlatformID string // 平台侧被 @ 的标识(username / open_id / qq number)
    AgentID    string // 解析命中的 Stella agent;解析不到则空
}
```

适配器填 `Raw` 和 `PlatformID`(它知道平台 id)。ingest best-effort 解析 `AgentID` 并存入 outbox envelope；dispatcher 在路由前对仍为空的 `AgentID` 再补解析一次。@路由只认 `Mention.AgentID != ""`——任何组件不在各处猜 username/open_id。

## 记忆:subject 轴(D4)

单独建群记忆表,**键 `(group_id)`——不做 per-agent**:

```sql
CREATE TABLE ctx_group_memory (
  group_id TEXT PRIMARY KEY,
  -- ... blob 抽屉,无 auth_user FK
);
```

三张现有用户记忆表(`ctx_agent_memory` / `_changelog` / `_snapshot`)完全不动。两个原因:

1. 那些表的 `user_id REFERENCES auth_user(id) ON DELETE CASCADE`。`group_id` 不是 `auth_user`,泛化进同表就得删 FK、丢掉「删用户级联清记忆」。
2. 单独建表**把隐私墙从纪律升级成类型系统**:DM 写路径根本拿不到 `ctx_group_memory` 的 handle,`private → group` 漏不了。

为什么 `(group_id)` 而非 `(group_id, agent_id)`?v1 抽取是通用的(不分 agent 角色),per-agent 抽屉只会把同一份抽取复制 N 份——无收益,还拖入 cursor agent 维度、membership 依赖、N× 成本。群记忆是群的共享知识,群内所有 agent 读同一抽屉。agent 专属群记忆是未来 additive 改动(届时加 `agent_id` 轴)。

写入规则按消息来源硬事实定(绝不交给 LLM):

| 消息来源         | 写进哪                                                       |
| ---------------- | ------------------------------------------------------------ |
| 用户群里公开发言 | 群共享抽屉 `(group_id)` **+** 该用户私有抽屉 `(user, agent)` |
| 用户私信         | 只私有抽屉——**永不**进群共享                                 |
| agent 发言       | 不写记忆                                                     |

`private → group` 是靠路径隔离强制的单向墙,不靠 prompt 求情。

## 记忆时间标签(D5)

`profile` 从整块 blob 改成带日期条目(对齐 constraints 现有的 `CreatedAt` 形态),且**日期必须渲染进 system prompt**(今天不渲染 = 白存)。HTTP 兼容:内部存条目,读接口把手动条目平铺回字符串,故 OpenAPI / SDK / UI 零改。

## 异步记忆 ingest(D6)

记忆绝不在回复链路里写。后台单消费者按 `seq > cursor` 从 event log 拉取、攒批、轻量 LLM 抽取、按 D4 路由。

- cursor:`ctx_group_ingest_cursor(group_id, pipeline)`,值 = 已消费 `seq`。
- dead-letter:`ctx_group_ingest_error(id, group_id, pipeline, seq, reason, created_at)`。瞬时失败(LLM 超时/限流)→ cursor 不前进、重试同一批;坏消息(无法解析)→ 写 dead-letter,cursor 越过该 seq。
- cursor 只前进到「已抽取或已 dead-letter」连续前缀末端,既不漏也不卡。

## Arbiter:发言闸门(D7)

群消息不再各 bot 直连 runtime。持久化链路:

```
群消息(任一 bot 送达)
  → 写 event log(D2 幂等;非首次插入则在此丢弃)
  → 创建 outbox work
  → 单一 group dispatcher
      → L0 规则闸门:已解析 @mention → 确定性响应者
      → L1 语义闸门:无 mention → fast-model JSON 决策
      → 物化 ctx_group_dispatch 行
      → 对选中的 agent 逐个:runtime → publisher(通过 reply_channel_id)
```

- 任何 `@mention` 信号都留在 L0 规则路径。已解析的 mention 绕过 `MaxRepliesPerTrigger`:用户一条消息 @ 多个群成员时,所有被 @ 的成员都会回复。平台 mention 无法解析到 Stella 群成员时不会被静默丢弃:dispatcher 会 fall-through 到同一条无 mention 语义路径,因此显式 `@mention` 绝不会比普通消息更不可靠。Web 文本 mention 解析不出成员时仍按普通文本进入同一条无 mention 路径。
- 无 mention 消息在配置了语义仲裁时走 L1 语义路由。分类器可以返回静默、单 agent 或受上限保护的多 agent 广播。失败、超时、无效 JSON 或无合格路由模型都折叠为静默。未配置语义仲裁时,唯一的自动回复是 Web 单成员群路由到唯一成员;其余任何群都静默,且多成员群会额外写 WARN 提示配置语义仲裁。
- L1 路由模型按归属选择,不是随便取第一个成员。Web 群优先群 owner 自己的 agent,再退到 system-scope agent。Platform 群只允许 system-scope agent,避免把私有 agent 的凭据用于共享路由决策。
- L1 只接收有界的公开路由元数据:agent ID/name、成员摘要(`system_prompt` 前 180 字符,有意发送以便正确路由),以及 `seq < currentSeq` 的有界历史群上下文。延迟 outbox 重试不会把未来消息当成历史上下文。
- decide 与 generate 分离,**decide 只出意图、不出草稿**(省 token)。
- 每次人类触发硬上限 N 条公开回复,防失控刷屏。
- agent 自己的发言写进 log(可被动读),但**默认不唤醒其他 agent 的 arbiter**。例外是显式 `@另一个 agent`,由独立 handoff dispatcher 处理——普通 arbiter 只对 `actor_type=human` 反应,两条路径互不冲突。

### Durable dispatch 正确性

- Platform ingest 在 event-log 消息同一事务内创建 pending outbox。Web 同步 ingest 在同一事务内以 `running` + lease 创建 outbox,避免后台 worker 在 SSE 路径执行时抢单。进程崩溃或租约过期后,worker 用 `NoopGroupPublisher` 恢复;Web 的持久交付来源是 event log,不是打开的 SSE socket。
- Web 断连不会取消生成。服务端使用带上限的服务生命周期 context,继续 drain stream,且只写回完整成功响应。取消或错误导致的 partial stream 不会 append。
- Dispatch 重试使用线性退避:`1s * attempts`,封顶 60s。超过重试预算后置为 `failed`;不会 fallback 到其它 channel 冒名该 agent。
- Dispatch 按 `(group_id, agent_id)` 保持顺序:SQL 只在同 agent 没有更小 `seq` 的 pending 或未过期 running 行时 claim。过期 running 行会被回收,不会永久阻塞。
- 回复发布是 at-least-once。正常收尾为 publish → 一个 DB 事务 append 群回复并写 `result_message_id` → 标记投递完成 → mark completed。剩余重复窗口是 publish 成功但 writeback+marker 事务尚未提交。
- **已生成的回复绝不重新生成。** 无论投递是否成功,回复都会先落库,所以 publish 失败也会留下 `result_message_id`。重试看到 `result_message_id` 且 `delivery_complete = false` 时,只重新投递这段已持久化的文本,永远不会再调用一次 Agent;看到 `delivery_complete` 则直接 completed。
- 重新投递从 `delivery_cursor` 续传,该游标记录发布方已确认被平台接收的前 N 个分块。Discord 按 2000 字符分块逐块确认,因此 3 块中第 2 块失败时只补发第 2、3 块;不做分块确认的平台整段重发。若某次尝试是重新生成而非重新投递,游标会被清零——它只对某一份具体回复有意义。
- 图片与文件不落库,也不会重新投递。上传失败只在群里提示并记 warn,不会让 publish 失败:为附件重排队会把 dispatch 推回到只能靠重跑 Agent 才能恢复的状态。
- running 状态的 dispatch 若持有 `result_message_id` 时租约过期,直接标记 completed 而不续传,避免一次心跳抖动在不做分块确认的平台上整段重发。
- 群上下文 injected 去重用 SQL 在整个 conversation 内做完整 content 精确查重,不依赖 token budget 窗口。

## 回复出口:只发到群(D8)

砍掉「agent 主动私信群成员」(平台禁止 bot 私信陌生人)。所有输出落群 ChatID;@点名是同一投递出口上的内容层处理。

## Session 归属(D9):为何必须设计期定

`ctx_conversation.session_id` 是 `UNIQUE`,LCM 查找是 `(session_id, user_id, agent_id)`。若共享 session 仍传当前发言人的 `user_id`:

- A 说 → 建行 `(S, A, agentX)`。
- B 说 → 查 `(S, B, agentX)` 落空 → create-if-missing 插 `session_id = S` → **撞 UNIQUE 约束。**

所以群 session 在 `ctx_conversation.user_id` 存 `group_id`,**仅作查找键**(该列无 `auth_user` FK)。`requireSessionScope` / `GetConversationBySessionID` 的群分支按 `(session_id, group_id, agent_id)` 匹配,不要求 actor 匹配。

关键约束:这个 `group_id` **绝不能流进成员私有的运行时身份面**。runtime 在一处识别「这是群 session」,把这些面统一改道:

| 面                      | 代码                                                      | 群 session 行为                                                                  |
| ----------------------- | --------------------------------------------------------- | -------------------------------------------------------------------------------- |
| memory / prompt profile | `runtime/chat.go`(`authz.WithUserID`)、`prompt/prompt.go` | 不注入任何 human;读群抽屉,绝不读成员私有 profile                                 |
| workspace               | `runner_builder.go`、`workspace.go`                       | 路径 `users/group-{group_id}/`——群是独立 principal,绝不是成员的 `users/{userID}` |
| vault                   | `sandbox/env.go`                                          | 只解 agent/群作用域密钥,绝不解成员私有 vault                                     |
| agent tools             | `authz.Identity` facade 与 tool handler                   | 以群 principal 执行,不是 human userID                                            |

不造 synthetic `auth_user`:群 principal 是执行作用域,不是 `auth_user` 表里的行。

membership 表闭环:

```sql
-- channel_group_member: PK (group_id, agent_id),另存 reply_channel_id
--   reply_channel_id FK -> channel(id)
--   写入 membership 与 publisher 发送前都断言 channel.agent_id == channel_group_member.agent_id
```

`channel.agent_id` 仍表示 bot→agent 绑定;`channel_agent` 的单 active 语义只留给 DM/非群。dispatcher 收到任一 bot 的消息,按 `group_id` 解析出该群全部 agent,各 agent 用自己的 `reply_channel_id` 回复。双重断言防止配置错/恶意写让 agentB 借 agentA 的 bot 发言。

## 当前发言人:逐轮个性化(D10)

D9 让群 session 保持匿名,没有任何真人拥有运行时。但 agent 仍需知道**此刻是谁在说话**才能个性化回复。这是第二条身份轴,刻意与运行时/session 身份分离,使它永远不会变成后者。

`memory.CurrentSpeaker` 携带逐轮发言人:`Platform`、`PlatformUserID`(仅查询/审计)、`DisplayName`、以及 `UserID`(发送者已关联时为解析出的 Stella 用户,未关联时为空)。它经由 `WithCurrentSpeaker` / `CurrentSpeakerFromContext` 走 context,与 `UserIDFromContext` 平行——绝不合并。

硬规则:

- **是个性化目标,不是运行时身份。** `CurrentSpeaker.UserID` 绝不可传给 `authz.WithUserID`、sandbox/vault/token 代码、plugin 或 delegate 上下文、notify 路由、hook 用户元数据。`runtime/chat.go` 为群聊回合附上发言人,但仍跳过 `WithUserID`,故 D9 四个面全部保持群作用域。
- **逐轮构建,绝不缓存。** prompt 的 `## Current Speaker` 段由 PoolManager 的 before-run prompt 重建逻辑每轮重渲整份系统提示词生成。缓存的群 runner 不持有发言人上下文,故一个发言人的回合元数据不会泄漏到另一个发言人的回合。
- **prompt 渲染按 `GroupID` 分支,而非按群记忆是否为空。** 群聊回合渲染 `## Group Memory`(+ 可选的 `## Current Speaker`),即使群抽屉为空也绝不回退到按用户的 `## User Profile` 段。
- **不自动注入私有 profile。** `## Current Speaker` 只暴露显示名与已关联/未关联状态,不包含发言人的 profile 正文、带日期条目、soul 或 constraints:公开群不是披露某成员私有记忆,也不是把某成员硬规则套到整群的地方。
- **按硬事实解析。** 平台发送者经渠道身份查找解析(已关联 → auth 用户 id;未关联 → 空 UserID → 仅名字)。Web 发送者仅当是真正的 human actor 时才信任已认证的 `actor_id` 作为发言人,否则 fail-closed。

`memory` 工具在群聊回合中对应:没有 session 用户时,普通聊天只能在模型显式调用工具时用只读 `profile_get` 回退到当前发言人；`profile_update` 只保留给显式开启写入的内部工具。`soul_*`、`constraint_*`、`profile_history` / `profile_rollback` 保持严格并 fail-closed。

## 实现顺序

数据模型(难改门)先行,行为层后接:

1. **Phase 1** —— IncomingMessage 字段(D3)。
2. **Phase 2** —— event log + 群 session 归属 DB 层(D2, D9 session scope)。**安全门:群 session 不接入 `Runtime.Chat`**——测试只在 schema/event-log/session-registry 层,避免 `group_id` 漏进运行时身份。
3. **Phase 2b** —— 群运行时身份隔离(D9 运行时面),横切上述八个文件;Phase 5 接线的前置。
4. **Phase 3** —— 群记忆表 + 时间标签(D4, D5)。
5. **Phase 4** —— 异步 ingest(D6)。
6. **Phase 5** —— 多 agent + arbiter(D1, D7),含 membership 表。
7. **Phase 6** —— 回复出口收口(D8)。

迁移一律走 schema 文件编辑 → `mise run db:diff` → `mise run generate`,绝不手写 SQL。
