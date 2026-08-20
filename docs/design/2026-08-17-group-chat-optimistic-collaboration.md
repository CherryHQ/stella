# 群聊多 Agent：乐观协作

群聊使用单一的乐观协作模型。每条 canonical 消息创建 outbox，再为除作者外的每个成员生成 wake。每个 agent 只领取自己的最新 wake；server triage hard caps、顺序和 freshness，并在接受事务中原子提交回复、memory 和后继 outbox。

Web 发送只负责 ingest 和 wake。回复与 presence 通过 group events 发布。过期回复标为 held，永不投递；silent、held 和 superseded 不推进 memory cursor。
