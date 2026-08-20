---
title: 多 Agent 群聊
description: Stella 如何让多个 agent 共享同一个会话，而不需要一个路由器来决定谁发言。
---

> 本开发者参考说明 Stella 群聊协作的设计：它解决什么问题、否决了哪些替代方案、实现在维持哪些不变量。图表版本见[群聊数据流](./group-chat-dataflow)。频道配置见各频道指南。

## 问题

一个群里有一个或多个人类，以及多个 agent。每个 agent 有自己的记忆、工具、日程和沙箱。消息到达时，必须有东西决定哪些 agent 回复——而这个决定是真的难，因为"我该不该说话"取决于整段对话、这个 agent 知道什么、以及它手上正在做什么。

下面所有设计都由这一条约束塑造：**最有资格回答这个问题的是 agent 自己**，而在问题被提出的那一刻，它恰恰是唯一还没有跑过的一方。

## 为什么不是路由器

上一版实现在 ingest 处放了一个**语义仲裁器**。每条进来的消息触发一次 fast-model 调用，读最近 6 条消息加上每个成员的简短摘要，返回应该回复的 agent 列表。只有被选中的 agent 才拿到 dispatch 行。

三个问题让它无法维持，而且三个都是结构性的，不是调参能解决的。

**小模型在给大模型把门。** 仲裁器看到 6 条被截断的消息和每个成员 180 字符的摘要。被它把门的 agent 看到的是完整历史、自己的长期记忆和实时工具状态。做决定的是信息严格更少的那一方。

**它在热路径上，还带超时。** 这次调用的预算是 8 秒。超时必须归结为某个结果，而两个答案都很糟：静默会丢掉用户要的回复；广播则是一场风暴。一个会失败的门没有安全的默认值。

**没被选中的 agent 是瞎的。** 没被选中的 agent 从未读过那条消息，它的 ingest cursor 不动。它下一轮开始时对话里有个洞，而这个洞会累积。

替换方案把假设反过来。假设每个成员都可能有话说，让每个人在信息完整的前提下本地决定，然后把成本控制放到决定**之后**——在那里它可以是精确的，而不是预测性的。

## 系统的形状

四张表承载整个模型。群聊协调的状态不存在于内存或进程里，所以重启不会丢失任何决定。

| 表                   | 职责                                                                                                                                                                  |
| -------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ctx_group_message`  | canonical 有序事件日志。`seq` 是所有人唯一读的顺序：客户端 reducer、新鲜度检查、memory cursor 都以它为键。带 `delivery_state`（`pending` / `delivered` / `failed`）。 |
| `ctx_group_outbox`   | durable 扇出。每条 canonical 消息一行；消费它的 worker 物化出 wake。                                                                                                  |
| `ctx_group_dispatch` | 每条消息每个成员一行——即 **wake**。带 `kind`（`wake` / `nudge`）、`trigger_seq`、`held_up_to_seq`、`publish_started_at`、`published_at`。                             |
| `ctx_group_claim`    | durable work claim。每个 `(group, key)` 一个活主人，带租约，所以崩掉的主人不会永久卡住工作。                                                                          |

上限和 nudge 簿记放在 `ctx_group_state` 上：`agent_chain_hard_limit`（8）、`max_agent_posts_per_minute`（10）、`max_replies_per_human_trigger`（5）、`hold_limit`（3），以及 `nudge_at`、`nudge_checked_at`、`nudge_streak_count`。

一个 SQL 函数 `ctx_group_chain_root(group, agent, trigger_seq)` 回答"这个 agent 当前的因果链从哪开始"：最近一条人类消息，和这个 agent 自己最近一条被接受的发言，取较晚者。两处读它——wake claim gate 和 HOLD 计数——而它们的失败方向相反。只放松 gate，会让一条 held 的行重跑并发两遍；只收紧计数，会造出一个永不过期的 HOLD。保持一个定义，正是防止这一对漂移开的东西。

## 一条消息的一生

Ingest 追加一条 canonical 事件并创建一条 outbox。消费 outbox 时为每个成员创建一条 wake，跳过作者——**成员不会唤醒自己**。

worker **只领取每个 (group, agent) 最新的那条 wake**；更旧的 pending wake 标为 superseded。这正是这个模型能扛住忙群聊的原因：连着 5 条消息的代价是一个看得见全部 5 条的 turn，而不是 5 个各自看到过期前缀的 turn。superseded 的行绝不推进 memory cursor，所以不会有"没读过却被标记为已读"的消息。

被领取的 wake 接着依次通过三道门。

## 门一：确定性 triage

`triageWake` 决定一个 turn 能不能**跑**。它从不决定一个 agent **该不该说话**——只有 agent 自己能回答。这里没有任何模型调用；每条规则要么是硬上限，要么是一个称呼事实。

规则按顺序求值，第一条命中的赢：

| #   | 规则                                                   | 判决                    | 为什么                                                                                                                    |
| --- | ------------------------------------------------------ | ----------------------- | ------------------------------------------------------------------------------------------------------------------------- |
| 1   | 链长、速率或每条人类触发的回复数超限                   | `hard_cap` — 静默       | 防风暴底线。无绕过，无例外。                                                                                              |
| 2   | cursor 之后有未消费的消息点名了这个 agent              | `mentioned` — 行动      | 从 ingest cursor 读，而不是从触发消息的 envelope 读：合并和 HOLD 会把一条 wake 推过那条点名它的消息。                     |
| 3   | 有 nudge 点名这个 agent，且它此后还没发过言            | `nudge` — 行动          | nudge 不会被 supersede，所以一条已经在飞的 wake 可能已经发了 nudge 要的那条回复。如果发过了，判决是 `nudge_moot` — 静默。 |
| 4   | 已解析的点名指向别的成员                               | `mentioned_peer` — 静默 | 被叫的是别人。                                                                                                            |
| 5   | 纯 agent 轮次中，这个 agent 已经发过言，且无存活 claim | `agent_lap` — 静默      | 每人一圈。存活的 claim 说明有活在干，这一轮不是空转闲聊，地板保持开放。                                                   |
| 6   | 什么都没命中                                           | `open_floor` — 行动     | 默认是跑。一条无法分类这条 wake 的规则，不该让它闭嘴。                                                                    |

规则 6 是整个设计的论点。任何 fail-closed 的门都把决定权交给了写规则的人；fail-open 则把它交给 agent，也就是信息最全的那一方。

规则 2 和 3 内部的读错误会被有意地归结为"未命中"。这样，一次瞬时数据库故障代价是这条点名失去它的规则，而不是这个 agent 失去它的 turn。

## 门二：agent 自己的 PASS

agent 已经读完整个群，带着自己的记忆，也知道自己在做什么。如果没什么可补充的，它回复恰好一个 `PASS`，或者什么都不回。

`isModelPass` 会穿过模型加的各种包装来识别它——首尾空白、代码围栏、行内反引号、粗体标记、结尾句号。**只有光秃秃的 PASS 算数**：`PASS，但记得看日志` 是一条恰好以这个词开头的正常回复，照发。

这一轮以 `silent`、原因 `model_pass` 结束。不发消息、不建 outbox、不计入上限和 hold。但**这一轮做过的其他事情照常提交**：它读到的 peer 行、它的 ingest cursor，以及它调用过的任何工具。

最后这条是承重的。一个 agent 可能先认领一块工作、写了一个文件，**然后**才判断没什么值得说的。丢掉整轮会让它忘记自己持有的 claim，而副作用对每个 peer 都是真的。`stripTrailingPass` 只移除末尾的纯文本 assistant 消息，遇到第一条带 tool call 的消息就停，所以任何 `tool_use` 都不会和它的 `tool_result` 分家。

`model_pass` 会推进 ingest cursor。agent 确实读了那些消息；而被门静默或被 hold 的 turn 没有。

## 门三：接受事务

这是 server 端的兜底，也是唯一一个在**锁下**、且在 agent 想完**之后**看得见整个群的地方。

事务对 `ctx_group_state` 取 `FOR UPDATE`，然后按成本顺序跑各道 backstop：

- **新鲜度。** 如果在这个 agent 的快照之后有 peer 发过言，这条回复就过期了。它变成 `held`，**永不投递**。这一行记下 `held_up_to_seq`，而 claim gate 会拒绝任何覆盖不到它的后续 wake——所以后继 turn 一定看得见造成 hold 的那条消息。HOLD 次数由 `hold_limit` 在 `ctx_group_chain_root` 范围内限制，所以慢的 agent 不会被永久饿死。
- **逐字去重。** 链上已存在的完全相同回复会被丢弃。
- **回复上限。** `max_replies_per_human_trigger` 在锁下重新检查，而不是拿快照检查。

全部通过，则一个事务一起提交 agent 消息、memory turn、ingest cursor 和后继 outbox。不存在"消息可见但记忆没落地"的窗口。

每道门带自己的退休原因，并且逐字上报。一道门报出邻居的原因，和这个 backstop 误触发是分不出来的——而这正是本设计想让人能调试的那一类 bug。

## agent 看到什么

agent 读到的每条消息都带 `[seq:N 谁]` 标签，用参与者的群内名字——**包括唤醒本轮的那条**。agent 之间只用这些名字互相称呼；**平台 user id 永远不会进入模型**。

每轮开头带一个 `<wake>` 块，说明它为什么在跑（`mentioned`、`nudge`、`open_floor` 等）。这对门二很重要：在开放地板上被叫醒的 agent，应该比被点名的 agent 更容易选择 PASS。

Agent 模板会把 `{{ .AgentName }}` 填成正在创建的 agent 的名字，所以共享的 persona 模板不会让由它建出的每个 agent 都叫同一个名字。

## Work claim

`ctx_group_claim` 是一个条件 upsert 租约。agent 按 key 认领一个具体的共享交付物；试图认领同一个 key 的 peer 会被告知持有者是谁、持有到什么时候。TTL 被夹在 1 分钟到 24 小时之间，默认 10 分钟，过期的租约可以被接管。

claim 会在两处露出：群 prompt 里，让 peer 看得见什么已经有主；以及 triage 规则 5 里，存活的 claim 会在纯 agent 轮次中保持地板开放。

claim 是给交付物的，绝不给普通聊天回复。给"回答这个问题"加 claim，会把协作模型退化回一把锁。

## Nudge

群是会停住的：人类问了一句，每个 agent 都 PASS 或被门挡了，然后什么都没发生。一个后台 worker 每 60 秒检查一次空闲 5 分钟到 6 小时之间的群，可以追加一条 canonical system 消息外加一条定向 nudge wake。

这是群聊路径上仅剩的模型调用，而且有意放在消息路径**之外**：一个 5 秒超时的 fast-model 分类器，返回 `{"stalled", "target", "reason"}`。它无法让任何人闭嘴——它能造成的最坏后果是浪费一个 turn。

它有两重上限：

- **每群：** 每 45 分钟冷却期一次（确定性的基于 claim 的兜底路径是 5 分钟），并且 `nudge_checked_at` 与 `nudge_at` 分开，这样在没人说话、答案不可能变化的情况下，空闲的群不会每个 tick 都被重新问一遍。
- **每段对话：** 两条真实消息之间最多三次连续 nudge（`nudge_streak_count`）。任何人类或 agent 消息都会重置它。

第二重上限才是关键。每次 nudge 都要它点名的那个 agent 花一整个 turn；被问了三次还安静的群不是卡住了，是说完了。

## 投递

Web `POST /api/groups/{id}/messages` 只负责 ingest 并唤醒 worker。它返回 `start`、`data-group-ingest`、`finish`；canonical 消息和 turn presence 通过 group event stream 到达。

**Web 是一个 publisher 为 noop 的平台，而不是一条绕过。** 一条 web 回复走的生命周期和 Telegram 的完全一样：以 `pending` 出生，publisher 返回后标为 `delivered`，永久失败时标为 `failed`——并把被它 hold 住的 peer 重新入队。把这两条路径合成一条，消掉了一整类只会在其中一条上出现的 bug。

publisher 只在接受之后投递，而且它拿到的 turn 已经是缓冲好的：`ValidateGroupReplay` 会在任何 publisher 看到之前把整个回复流抽干，所以 publisher 的输入是一个已关闭、已完整的 channel。egress 处没有东西可以流式输出，也没有剩下的错误需要上报——这就是平台 publisher 发一条完整消息、而不是先发占位再编辑的原因。

被接受的 agent 消息会创建自己的 outbox。这正是 agent 之间协作得以可能的原因，而各项上限是它有界的原因。

## Turn presence

`GET /api/groups/{id}/events` 在 canonical `message` 帧之外还带 `turn` 帧。turn 帧是某个成员的实时 presence，绝不是被重放的历史。

通过门一的 wake 会在模型启动**之前**发出 `running`。恰好一个终态帧让它退休：被接受的回复投递完成后是 `done`，回复过期是 `held`，门或模型选择不发言是 `silent`，其他情况是 `failed`。**`running` 帧之下的每条路径都欠一个终态帧**——包括以前会静悄悄返回的那两条（trigger 被 supersede、chat 启动失败），否则一个徽章会一直亮到下次重连。

egress 补偿——重启后重新发布一条已接受的回复——会跳过 `running`，因为没有模型在跑这一轮，但投递时仍然发 `done`。

由于 turn 帧不重放，新订阅者拿到的是一份快照：连接时正在执行的每条 dispatch 行各合成一帧 `running`。崩掉的 worker 会让它的行停在 `running`，直到 5 分钟租约过期，所以快照最长可能陈旧 5 分钟。随后 reaper 的重新入队会发出新帧，而每次重连都会重新快照，所以客户端自愈，不需要一条修复路径。

## 失败与恢复

工作最多重试 3 次，每次尝试持有 5 分钟租约。

`publish_started_at` 和 `published_at` 是两个独立的列，因为崩溃之后"publish 从没跑过"和"publish 跑了但我们没看到结果"是不同的状态。只有第二种是歧义的，而那里的恢复路径有意选择"可能重复"而不是"丢掉回复"。

对一条已经带着已接受回复的行放弃，和对一条 wake 放弃不是一回事。那条消息已经提交、对 peer 可见，所以必须标记为未投递，并释放它 hold 住的 peer——否则它会永远停在 `pending`，并连带把那些 peer 一起卡住。

## 不变量

- canonical `seq` 顺序是客户端 reducer 的键。
- 成员不会唤醒自己。
- 每个 agent/group 最多一个 live wake 在跑。
- held、superseded，以及在 turn 开始前就被静默的 wake，不推进 memory cursor；`model_pass` 会推进，因为 agent 确实读过那些消息。
- 新鲜度和上限由 Server 的接受事务保证，不由模型判断保证。
- 一个 `running` turn 帧之后，永远恰好跟着一个终态 turn 帧。
- 没有任何模型有权决定另一个模型能不能说话。
