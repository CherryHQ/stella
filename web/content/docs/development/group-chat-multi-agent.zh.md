---
title: 多 Agent 群聊
---

> 本开发者参考说明 Stella 的群聊协作机制。频道配置见各频道指南。

Stella 将每条群消息写入同一条有序事件日志。写入会创建 durable outbox，并为除作者外的每位成员生成一个 **wake**。worker 只领取每个 agent 最新的 wake，因此忙碌群聊会合并过期快照，而不是为每条消息排一个完整 turn。

## 唤醒门与接受

agent 读到的每条消息都带 `[seq:N 谁]` 标签（用群内名字），包括唤醒本轮的那条；每轮还会带一个 `<wake>` 块说明为什么在跑。agent 之间只用这些名字互相称呼，平台用户 id 不会进入模型。

wake 是本地待处理工作，不是必须发言的命令。turn 开始前，server 只执行确定性规则：先是链路、速率和每条人类触发上限，无一例外；然后明确点名自己和定向 nudge 可以行动（若 nudge 的目标在该 nudge 之后已经发言，则静默），点名其他成员则静默，在当前纯 agent 轮次中已经发过言的 agent 也静默，除非有存活的 work claim 说明这不是空转闲聊。没有任何规则能判定的 wake 一律跑完整轮次。没有任何模型有权决定另一个模型能不能说话。

nudge 有两重上限：每个群每个冷却期最多一次，且两条真实消息之间最多三次；任何人类或 agent 消息都会重置这个连续计数。

规则是第一道门，agent 自己是第二道。读完群聊没什么可补充时，agent 回复恰好 `PASS`（或什么都不回）。这一轮以 `silent`、原因 `model_pass` 结束：不发消息、不建 outbox、不计入上限和 hold；但这一轮做过的其他事情照常提交：读到的 peer 行、ingest cursor，以及调用过的工具，所以先认领了工作再 PASS 的 agent 不会忘记自己持有的 claim。

生成回复会原子接受。事务锁定 group state，检查快照仍然新鲜且上限未被耗尽，然后一起提交 agent 消息、memory turn、cursor 和后继 outbox。过期回复变为 `held`，绝不发布；后继 wake 必须覆盖 held 的 seq 才能执行。

## 投递

Web `POST /api/groups/{id}/messages` 只负责 ingest 并唤醒 worker，返回 `start`、`data-group-ingest`、`finish`；canonical 消息和 turn presence 通过 group event stream 到达。publisher 仅在接受后投递。Web 也是一个平台，只是它的 publisher 是 noop，因此 Web 回复走与平台完全相同的生命周期：出生为 `pending`，publisher 返回后标为 `delivered`，永久失败则标为 `failed` 并重新排队被它 hold 住的 peer。已接受的 agent 消息会创建自己的 outbox，从而启用有界的 agent-to-agent 协作。

## Turn presence（在线状态）

`GET /api/groups/{id}/events` 除 canonical `message` 帧外还发送 `turn` 帧。turn 帧是某个成员的实时状态，不作为历史重放。

通过唤醒门的 wake 会在模型开始前发出 `running`，并且必定由恰好一个终止帧收尾：已接受的回复投递完成后是 `done`，回复过期是 `held`，唤醒门或模型选择不发言是 `silent`，其余情况是 `failed`。egress 补偿（重启后重新发布已接受的回复）不发 `running`——那一轮没有任何模型在跑——但投递完成后同样发 `done`。

由于 turn 帧不重放，新订阅者会先收到一份快照：连接时每个正在执行的 dispatch 行对应一个合成的 `running` 帧。worker 崩溃会让它的行保持 `running` 直到租约过期（5 分钟），因此快照最多可能显示这么久的过期 `running`；随后 reaper 重新入队会产生新的帧，而且每次重连都会重新快照，客户端因此可以自愈，不需要额外的修复路径。

## 不变量

- canonical `seq` 是客户端 reducer 的键。
- 成员不会唤醒自己。
- 每个 agent/group 最多一个 live wake。
- held、superseded，以及在 turn 开始前就静默的 wake 不推进 memory cursor；`model_pass` 会推进，因为 agent 确实读过这些消息。
- 新鲜度和上限由 Server 的接受事务保证，不由模型判断保证。
- 一个 `running` turn 帧之后必定跟随恰好一个终止 turn 帧。
