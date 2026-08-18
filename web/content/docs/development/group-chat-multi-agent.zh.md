---
title: 多 Agent 群聊
---

> 本开发者参考说明 Stella 的群聊协作机制。频道配置见各频道指南。

Stella 将每条群消息写入同一条有序事件日志。写入会创建 durable outbox，并为除作者外的每位成员生成一个 **wake**。worker 只领取每个 agent 最新的 wake，因此忙碌群聊会合并过期快照，而不是为每条消息排一个完整 turn。

## 本地 triage 与接受

wake 是本地待处理工作，不是必须发言的命令。Server 先执行链路、速率和每条人类触发上限。明确点名自己和定向 nudge 可以行动，点名其他成员则静默。只有一名 agent 的 Web 群在人类消息下可行动。其他未点名 wake 默认静默，直到未来的群策略给出本地行动理由。

生成回复会原子接受。事务锁定 group state，检查快照仍然新鲜且上限未被耗尽，然后一起提交 agent 消息、memory turn、cursor 和后继 outbox。过期回复变为 `held`，绝不发布；后继 wake 必须覆盖 held 的 seq 才能执行。

## 投递

Web `POST /api/groups/{id}/messages` 只负责 ingest 并唤醒 worker，返回 `start`、`data-group-ingest`、`finish`；canonical 消息和 turn presence 通过 group event stream 到达。平台 publisher 仅在接受后投递。已接受的 agent 消息会创建自己的 outbox，从而启用有界的 agent-to-agent 协作。

## 不变量

- canonical `seq` 是客户端 reducer 的键。
- 成员不会唤醒自己。
- 每个 agent/group 最多一个 live wake。
- silent、held、superseded wake 不推进 memory cursor。
- 新鲜度和上限由 Server 的接受事务保证，不由模型判断保证。
