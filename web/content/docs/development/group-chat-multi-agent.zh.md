---
title: 多 Agent 群聊
description: Stella 如何让多个 agent 共享同一个会话，而不需要一个路由器来决定谁发言。
---

> 本开发者参考说明 Stella 群聊协作的设计：它解决什么问题、否决了哪些替代方案、实现在维持哪些不变量。图表版本见[群聊数据流](./group-chat-dataflow)。频道配置见各频道指南。

## 问题

一个群里有一个或多个人类，以及多个 agent。每个 agent 有自己的记忆、工具、日程和沙箱。消息到达时，必须有东西决定哪些 agent 回复——而这个决定是真的难，因为“我该不该说话”取决于整段对话、这个 agent 知道什么、以及它手上正在做什么。

下面所有设计都由这一条约束塑造：**最有资格回答这个问题的是 agent 自己**，而在问题被提出的那一刻，它恰恰是唯一还没有跑过的一方。

## 为什么不是路由器

上一版实现在 ingest 处放了一个_语义仲裁器_。每条进来的消息触发一次 fast-model 调用，读最近 6 条消息加上每个成员的简短摘要，返回应该回复的 agent 列表。只有被选中的 agent 才拿到 dispatch 行。

三个问题让它无法维持，而且三个都是结构性的，不是调参能解决的。

**小模型在给大模型把门。** 仲裁器看到 6 条被截断的消息和每个成员 180 字符的摘要。被它把门的 agent 看到的是完整历史、自己的长期记忆和实时工具状态。做决定的是信息严格更少的那一方。

**它在热路径上，还带超时。** 这次调用的预算是 8 秒。超时必须归结为某个结果，而两个答案都很糟：静默会丢掉用户要的回复；广播则是一场风暴。一个会失败的门没有安全的默认值。

**没被选中的 agent 是瞎的。** 没被选中的 agent 从未读过那条消息，它的 ingest cursor 不动。它下一轮开始时对话里有个洞，而这个洞会累积。

替换方案把假设反过来。假设每个成员都可能有话说，让每个人在信息完整的前提下本地决定，然后把成本控制放到决定**之后**——在那里它可以是精确的，而不是预测性的。

## 系统的形状

四张表承载事件与工作台账。协调还依赖 `ctx_group_state` 保存加锁的上限/nudge 状态，以及 `ctx_group_ingest_cursor` 保存每个 agent 的 durable 读取边界。没有承重状态只存在于进程内存里，所以重启不会丢失任何决定。

| 表                   | 职责                                                                                                                                                                                                          |
| -------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ctx_group_message`  | canonical 有序事件日志。`seq` 是所有人唯一读的顺序：客户端 reducer、新鲜度检查、memory cursor 都以它为键。带 `delivery_state`（`pending` / `delivered` / `failed`）。                                         |
| `ctx_group_outbox`   | durable 扇出来源。ingest 为自己的 canonical 消息创建一行；已投递的 agent 回复只会在 publisher 成功 finalize 的事务中得到 peer outbox。                                                                        |
| `ctx_group_dispatch` | durable wake 台账。普通扇出为每个合格成员各建一行，但跳过 agent 作者；定向 nudge 则只建给目标的一行。行带 `kind`（`wake` / `nudge`）、`trigger_seq`、`held_up_to_seq`、`publish_started_at`、`published_at`。 |
| `ctx_group_claim`    | durable work claim。每个 `(group, key)` 一个活主人，带租约，所以崩掉的主人不会永久卡住工作。                                                                                                                  |

上限和 nudge 簿记放在 `ctx_group_state` 上：`agent_chain_hard_limit`（8）、`max_agent_posts_per_minute`（**每个 agent 默认 10，可配 1–1000**）、`max_replies_per_human_trigger`（5）、`hold_limit`（3），以及 `nudge_at`、`nudge_checked_at`、`nudge_streak_count`。

一个 SQL 函数 `ctx_group_chain_root(group, agent, trigger_seq)` 回答“这个 agent 当前的因果链从哪开始”：`trigger_seq` 之前最近一条人类消息，和这个 agent 自己最近一条被接受的发言，取较晚者。trigger 上界是有意不对称的：它只约束 human 分支，accepted-post 分支读取 agent 最新已提交的结果。它有四个消费者：wake claim 的 held 覆盖 gate、HOLD 预算计数、wake 的 `held_up_to_seq` 提示，以及按链范围做的逐字去重。它们失败的方式不同：只放松 claim gate，会让一条 held 的行重跑并发两遍；只收紧计数，会造出一个永不过期的 HOLD；把去重范围放宽，则会永远压掉一句普通确认。保持一个定义，正是防止这四处漂移开的东西。

## 一条消息的一生

Ingest 追加一条 canonical 事件并创建一条 outbox。消费普通 outbox 时，为每个合格成员物化一条 wake，跳过 agent 作者——**成员不会唤醒自己**。nudge outbox 则只物化给它点名目标的一条 wake；这个目标例外意味着 outbox 和 dispatch 的基数都不是简单的“每成员一行”。

worker 会把同一个 `(group, agent)` 的普通 wake 合并到最新一条，并把更旧的 pending wake 标为 `status='superseded'`。定向 nudge 永不以这种方式被合并。不过两种 kind 共享**每个 `(group, agent)` 一个、不区分 kind 的 live slot**：wake 和 nudge 不能在同一群中为同一个 agent 并发执行。这正是这个模型能扛住忙群聊的原因：连着 5 条消息的代价是一个看得见全部 5 条的 turn，而不是 5 个各自看到过期前缀的 turn。被 claim 阶段合并而 supersede 的行绝不推进 memory cursor，也不发 turn frame，所以不会有“没读过却被标记为已读”的消息。

worker 拿到 queue slot 后，会重查定向 nudge 是否已经 moot。nudge 等待期间，一条普通 wake 可能已经发出了它要的那条回复；这条 nudge 会静默退休，不再消耗一个 turn。

被领取的 wake 接着依次通过三道门。

## 门一：确定性 triage

`triageWake` 决定一个 turn 能不能**跑**。它从不决定一个 agent **该不该说话**——只有 agent 自己能回答。这里没有任何模型调用；每条规则要么是硬上限，要么是一个称呼事实。

规则按顺序求值，第一条命中的赢：

| #   | 规则                                                   | 判决                    | 为什么                                                                                                                                      |
| --- | ------------------------------------------------------ | ----------------------- | ------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | 链长、速率或每条人类触发的回复数超限                   | `hard_cap` — 静默       | 防风暴底线。无绕过，无例外。速率上限是每个 agent 每分钟 10 条。                                                                             |
| 2   | cursor 之后有未消费的消息点名了这个 agent              | `mentioned` — 行动      | 从 ingest cursor 读，而不只读触发消息的 envelope：合并可能让一条 wake 越过那条点名它的消息。                                                |
| 3   | 有 nudge 点名这个 agent                                | `nudge` — 行动          | nudge 不会被 supersede，所以一条已经在飞的 wake 可能已经发了 nudge 要的那条回复。拿到 queue slot 后重查若为真，判决是 `nudge_moot` — 静默。 |
| 4   | 这条 wake 覆盖了尚未消费的旧 HOLD                      | `held_successor` — 行动 | 被 hold 的 turn 已消费自己的 mention 和 history；在 cursor 覆盖 `held_up_to_seq` 前，必需的后继由 durable held 行准入。                     |
| 5   | 已解析的点名指向别的成员                               | `mentioned_peer` — 静默 | 被叫的是别人，除非这条 wake 仍欠着一个 HOLD 后继 turn。                                                                                     |
| 6   | 纯 agent 轮次中，这个 agent 已经发过言，且无存活 claim | `agent_lap` — 静默      | 每人一圈。存活的 claim 说明有活在干，这一轮不是空转闲聊，地板保持开放。                                                                     |
| 7   | 什么都没命中                                           | `open_floor` — 行动     | 默认是跑。一条无法分类这条 wake 的规则，不该让它闭嘴。                                                                                      |

规则 7 是整个设计的论点。任何 fail-closed 的门都把决定权交给了写规则的人；fail-open 则把它交给 agent，也就是信息最全的那一方。

规则 2 内部的读错误会被有意地归结为“未命中”。这样，一次瞬时数据库故障代价是这条点名失去它的规则，而不是这个 agent 失去它的 turn。规则 1 之前读取上限数据失败，以及规则 4 读取 durable HOLD 失败则不同：triage 返回 `triage_db_error`，还有尝试预算时重新入队，普通三次尝试耗尽后才静默退休。nudge-moot 读取发生得更晚，在 session queue 内；那里读取失败会让 dispatch 失败并重试，而不是冒险重复工作。

## 门二：agent 自己的 PASS

agent 已经读完整个群，带着自己的记忆，也知道自己在做什么。如果没什么可补充的，它回复恰好一个 `PASS`，或者什么都不回。

`isModelPass` 会穿过模型加的各种包装来识别它——首尾空白、代码围栏、行内反引号、粗体标记、结尾句号。**只有光秃秃的 PASS 算数**：`PASS，但记得看日志` 是一条恰好以这个词开头的正常回复，照发。

这一轮以 `silent`、原因 `model_pass` 结束。不发消息、不建 outbox、不计入上限和 hold。但**这一轮做过的其他事情照常提交**：它读到的 peer 行、它的 ingest cursor，以及它调用过的任何工具。

最后这条是承重的。一个 agent 可能先认领一块工作、写了一个文件，**然后**才判断没什么值得说的。丢掉整轮会让它忘记自己持有的 claim，而副作用对每个 peer 都是真的。`stripTrailingPass` 只移除末尾的纯文本 assistant 消息，遇到第一条带 tool call 的消息就停，所以任何 `tool_use` 都不会和它的 `tool_result` 分家。

`model_pass` 会推进 ingest cursor。post-turn backstop 也会：`held`、duplicate 或 cap 判决之后，事务会提交 agent 实际读过的 history 和 tool 轨迹，并把 cursor 推进到 `trigger_seq`；它只丢掉没有被接受的末尾回复。因为 HOLD 因而已经消费了原始 mention，它的后继会在 peer-mention 和 lap triage 有机会静默之前，由覆盖范围内的 durable held 行以 `held_successor` 准入。一旦该后继提交的 cursor 覆盖 `held_up_to_seq`，旧 HOLD 就不再提供准入。claim 阶段 superseded 的行或 turn 开始前的 gate decline 没有跑模型，所以两者都不推进 cursor。

## 门三：接受事务

这是 server 端的兜底，也是唯一一个在**锁下**、且在 agent 想完**之后**看得见整个群的地方。

事务对 `ctx_group_state` 取 `FOR UPDATE`，然后按成本顺序跑各道 backstop：

- **新鲜度。** 在 `ctx_group_chain_root` 内发生的 HOLD 少于 `hold_limit` 时，如果 agent 快照之后有 peer 发言，这条回复就变成 `held`，**永不投递**。旧行通常会带着 `held_up_to_seq` 保持 held；之后另一条独立的 pending wake，且其快照覆盖该 seq，才是后继 turn。补偿例外是已接受投递的终态失败，它只会重新入队被该失败 post 因果性 hold 的行。HOLD 上限耗尽后，新鲜度不再拦住这条过期回复：它继续经过去重和上限检查，通常会被接受。
- **逐字去重。** 链上已存在的完全相同回复以 `silent`、原因 `duplicate` 退休；它不消耗 HOLD 预算。
- **链和回复上限。** `agent_chain_hard_limit` 与 `max_replies_per_human_trigger` 都在锁下重新检查，而不是拿快照检查；若用尽则以 `silent` 退休。

全部通过，则一个事务一起提交**`pending`** 的 agent 消息、deferred memory turn 及其 cursor、以及 dispatch result marker。这个事务刻意不建 peer outbox：这条回复本身要等 publisher 成功返回后才会唤醒 peer。publisher 的 finalize 事务会一起把消息标为 `delivered` 并创建 peer outbox。不过 pending canonical 行已经存在，所以被后续独立活动唤醒的 peer 仍可能在投递结果确定前遇到它。严格的原子保证是：被接受的消息绝不会在自己的 agent memory 尚未落地时可见。

每道门带自己的退休原因，并且逐字上报。一道门报出邻居的原因，和这个 backstop 误触发是分不出来的——而这正是本设计想让人能调试的那一类 bug。

## agent 看到什么

agent 读到的每条消息都带 `[seq:N 谁]` 标签，用参与者的群内名字——**包括唤醒本轮的那条**。agent 之间只用这些名字互相称呼。参与者命名会尝试平台 identity 和账户名，但解析永不失败：如果查询或平台 ID 解析无法得到名字，模型会看到稳定的原始 actor ID。

每轮开头带一个 `<wake>` 块，说明它为什么在跑（`mentioned`、`nudge`、`open_floor` 等）。这对门二很重要：在开放地板上被叫醒的 agent，应该比被点名的 agent 更容易选择 PASS。

Agent 模板会把 `{{ .AgentName }}` 填成正在创建的 agent 的名字，所以共享的 persona 模板不会让由它建出的每个 agent 都叫同一个名字。

## Work claim

`ctx_group_claim` 是一个条件 upsert 租约。agent 按 key 认领一个具体的共享交付物；试图认领同一个 key 的 peer 会被告知持有者是谁、持有到什么时候。TTL 被夹在 1 分钟到 24 小时之间，默认 10 分钟，过期的租约可以被接管。

claim 会在两处露出：群 prompt 里，让 peer 看得见什么已经有主；以及 triage 规则 6 里，存活的 claim 会在纯 agent 轮次中保持地板开放。

claim 是给交付物的，绝不给普通聊天回复。给“回答这个问题”加 claim，会把协作模型退化回一把锁。

## Nudge

群是会停住的：人类问了一句，每个 agent 都 PASS 或被门挡了，然后什么都没发生。一个后台 worker 每 60 秒检查一次空闲 5 分钟到 6 小时之间的群，可以追加一条 canonical system 消息外加一条定向 nudge wake。

这是群聊路径上仅剩的模型调用，而且有意放在消息路径**之外**：一个 5 秒超时的 fast-model 分类器，返回 `{"stalled", "target", "reason"}`。它无法让任何人闭嘴——它能造成的最坏后果是浪费一个 turn。

它有三重上限：

- **候选工作量：** 每轮最多读取 50 个候选。`nudge_checked_at` 与 `nudge_at` 分开：新活动发生后，或过了 30 分钟重查期，才会重新考虑一个群；没有变化时不会每 tick 都重问。
- **每群：** 每 45 分钟冷却期一次（确定性的基于 claim 的兜底路径是 5 分钟）。
- **每段对话：** 两条真实消息之间最多三次连续 nudge（`nudge_streak_count`）。任何人类或 agent 消息都会重置它。

确定性 fallback 只在 classifier 不可用时启用。它只会在这一轮恰好有一个候选、其最新消息不超过 30 分钟前，且该群恰好有一个 live claim、其 owner 又不是最后发言者时继续；它使用 5 分钟冷却期。这个窄形状避免一次故障把一个 batch 变成广泛、臆测性的恢复扫描。

streak 上限才是关键。只有通过 queue-slot moot 复检后仍然存活的 nudge，才会让它点名的 agent 花一整个 turn；连续三次这样的尝试后仍然安静的群不是卡住了，是说完了。

## 投递

Web `POST /api/groups/{id}/messages` 只负责 ingest 并唤醒 worker。它返回 `start`、`data-group-ingest`、`finish`；canonical 消息和 turn presence 通过 group event stream 到达。

**Web 是一个 publisher 为 noop 的平台，而不是一条绕过。** 一条 web 回复走的生命周期和 Telegram 的完全一样：以 `pending` 出生，在 publisher 成功 finalize 的事务中标为 `delivered`，已接受的投递永久不能完成时标为 `failed`。只有这种已接受投递失败会释放被该回复 hold 住的 peer；一条接受前就失败的普通 wake 没有已接受的 peer post 可以补偿。失败的 canonical 行会留在作者自己的 memory 中，维持工具/history 一致性，但不会注入任何 peer 的 transcript，因为群对话从未收到它。

真正的预缓冲是 `bufferGroupResponse`：它在接受和任何平台副作用之前，抽干 runtime 的完整 event stream，受 8 MiB 内存上限约束。publisher 拿到的是已经关闭的 replay。`ValidateGroupReplay` 是 publisher 侧对该 replay 的防御性复检，不是缓冲实时模型流的机制。egress 处不再有实时模型流或模型错误；平台投递本身仍可能失败，并由 dispatch 重试状态机处理。这就是平台 publisher 发一条完整消息、而不是先发占位再编辑的原因。

已投递的 agent 消息会在同一个 finalize 事务中创建自己的 outbox。这正是 agent 之间协作得以可能、且能在所有平台一致发生的原因，而各项上限是它有界的原因。

## Turn presence

`GET /api/groups/{id}/events` 在 canonical `message` 帧之外还带 `turn` 帧。turn 帧是某个成员的实时 presence，绝不是被重放的历史。

一行被 claim 后，数据库状态先变成 `running`。wake 或 nudge 只有在拿到 per-session queue slot、chat stream 已经启动后才发 `running` 帧；排队期间变成 moot 的 nudge 只发终态 `silent`。gate decline 同样从一条数据库中已经 `running` 的行发出终态 `silent`，但绝不发 `running` 帧。被 claim 阶段 wake 合并标成 `status='superseded'` 的行完全不发 turn frame。另一种情况是：已经 claim 的行发现自己的 trigger 跨 session boundary 被消费，它会发一帧终态 `silent`，reason 同样叫 `superseded`；这是判决原因，不是数据库里的 superseded 状态。订阅者一旦通过实时帧或 presence 快照见过 `running`，恰好一个终态帧让它退休：已接受回复投递完成后是 `done`，新鲜度拦住回复是 `held`，模型或后续 backstop 选择不发言是 `silent`，其他情况是 `failed`。

egress 补偿——重启后重新发布一条已接受的回复——会跳过 `running`，因为没有模型在跑这一轮，但投递时仍然发 `done`。

由于 turn 帧不重放，新订阅者拿到的是一份快照：连接时每个存在执行中 dispatch 的不同 agent 各合成一帧 `running`。崩掉的 worker 会让它的行停在 `running`，直到 5 分钟租约过期，所以快照最长可能陈旧 5 分钟。随后 reaper 会在重新入队或标记失败前，用一帧终态 `failed` 退休这次尝试；每次重连都会重新快照，所以客户端自愈，不需要一条修复路径。

## 失败与恢复

普通 wake 最多 3 次尝试：初次尝试加 2 次重试；每次尝试持有 5 分钟租约。

`publish_started_at` 和 `published_at` 是两个独立的列，因为崩溃之后“publish 从没跑过”和“publish 跑了但我们没看到结果”是不同的状态。只有第二种是歧义的；它对应的已接受回复最多可恢复到 10 次尝试。这里有意选择“可能重复”而不是“丢掉回复”。一旦 `published_at` 已 durable 落地，平台投递就是已知成功；本地 delivered/outbox finalize 是幂等的，会持续重试，而不会把已投递消息错误标成 failed。

对一条已经带着已接受回复的行放弃，和对一条 wake 放弃不是一回事。已接受投递的终态失败会把消息标记为 `failed`，并释放它 hold 住的 peer；普通 wake 的失败两者都不会做。

## 不变量

- canonical `seq` 顺序是客户端 reducer 的键。
- 成员不会唤醒自己：普通扇出跳过 agent 作者，nudge 只指向一个点名成员。
- 每个 agent/group 最多一个 live dispatch，不区分 wake 或 nudge。
- superseded 行和 turn 前 gate decline 不推进 memory cursor。model pass 和 post-turn backstop 会提交实际读到的 history/tool 工作到 `trigger_seq`，但不保留被拒绝的末尾回复。
- 新鲜度和上限由 Server 的接受事务保证，不由模型判断保证。
- 一个 `running` turn 帧之后，永远恰好跟着一个终态 turn 帧；gate decline 和 superseded 行都不会产生这个 `running` 帧。
- 没有任何模型有权决定另一个模型能不能说话。
