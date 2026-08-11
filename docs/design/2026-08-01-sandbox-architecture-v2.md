# Sandbox 架构 v2：共享 POSIX Workspace 与多副本执行

- **日期**：2026-08-01（2026-08-11 重写）
- **状态**：当前架构来源；#862 已以 merge commit `d05375f4e28b364a5023cdf6e15ccf4b83f9d378` 合并，#886 是当前实现
- **实施计划**：`docs/design/2026-08-02-sandbox-architecture-v2-implementation-plan.md`
- **范围**：持久 Workspace、Sandbox 边界、多副本生命周期、Kubernetes、文件消费者与 Skill authority

本文定义当前目标，不以免责声明覆盖旧方案。历史版本仍可从 Git 历史查看。

## 1. 核心模型

Stella 的 mutable Workspace 文件代码只依赖 `WorkspaceManager` 暴露的、类型化且确定性的 POSIX root。业务身份决定 root；调用方不保存或拼接物理位置。Immutable builtin bundle 与 `BlobStore`/S3 consumer 不通过 `WorkspaceManager`，除非它们正在把授权的 mutable input 发布为 immutable snapshot。

```text
PrincipalRoot(user, principal_id)
PrincipalRoot(group, principal_id)
PrincipalAgentRoot(principal_kind, principal_id, agent_id)
GlobalAgentRoot(agent_id)
```

- user 与 group 即使 raw ID 相同也处于不同 namespace。
- principal-Agent root 隔离同一 principal 下的不同 Agent。
- global-Agent root 适用于不属于某个 principal 的 Agent 数据。
- user-less 执行没有可写 principal root。global-Agent root 只按明确策略提供定义或只读共享资源，不是通用 writable workspace；执行只写明确授权的 project root 和 disposable scratch。
- 并发读写遵循普通 POSIX 语义。Stella 不增加 MVCC、全局写锁、自动 merge 或透明重试。

单副本部署使用一个本地 POSIX `$STELLA_HOME`。多副本部署仍使用完全相同的逻辑 namespace 和 `WorkspaceManager` 契约，但所有 `stellad` 副本与 Session compute 必须看到一个全局共享、强一致的 POSIX filesystem。JuiceFS CE 是推荐的部署实现，不是 Stella 应用依赖；EFS、NFS、CephFS 或其他实现只要通过 conformance 均可使用。

S3 不是 live Workspace API。它继续用于 immutable `BlobStore`、artifact、session media 与发布时生成的 share snapshot，也可以是共享 POSIX 实现背后的内部介质。

## 2. Authority 与持久化边界

PostgreSQL 保存：

- user、group、Agent、Session 等业务身份、owner、authorization 与配置；
- channel receipt/FIFO、GroupRoute、Publisher 所需业务坐标和控制状态；
- 未来多副本所需的 AgentRun、Sandbox compute generation 与 distributed lifecycle lease；
- Skill policy、确有必要的窄化迁移状态，以及 Reflect 业务 telemetry。

Mutable filesystem bytes 以确定性 POSIX root 为 authority。PostgreSQL 可以保存消息、配置、immutable blob 引用等业务内容；“文件字节不进 PG”不等于“PG 不保存任何内容”。

`SandboxRef.generation` 只标识 compute 实例，用于拒绝 stale executor 和旧 Pod/Container。它永远不定位 Workspace 数据。Workspace API 只做 authorization 后通过 `WorkspaceManager` 操作持久 root，不获取 AgentRun，也不获取或唤醒某个 Sandbox generation。

当前单副本 Owner 删除先通过 process-local lifecycle gate 阻止新的相关使用，再删除业务 owner；文件字节与 inode 保留。root 的文件系统占用也继续保留 Agent ID，不能让新 Agent 附着到旧字节。多副本启用前还必须由 PostgreSQL generation/lease fence 所有副本。未来可选清理只能是 stopped/fenced 的 operator maintenance；它不是在线业务生命周期。

## 3. 已合并基础：#862

#862 已实现并合并以下单副本正确性基础：

- 唯一注入的 `WorkspaceManager` 是生产代码的 sole materializer；
- user、group、principal-Agent、global-Agent root 均由类型化身份确定；
- 创建前验证 PostgreSQL durable owner，owner 查询失败时 fail closed 且不修改文件系统；
- 以 pinned root FD、`openat` 和 no-follow 检查抵抗路径穿越、symlink、错误文件类型和可信 root 替换；
- 所有生产 Service 共享 process-local、writer-progress lifecycle gate；
- owner 删除保留 bytes/inodes，删除后的访问因 owner 校验失败；
- 任意文件、目录或 symlink 对 global Agent root 的占用都阻止 Agent ID 重新附着。

代码、build 和 focused tests 已通过。`mise run system-test` 在受支持 host 上仍是未完成验收项；orb 中缺少可工作的 Bubblewrap，因此不能把 #862 描述为完整通过 system-test。

#862 同时明确移除了旧设计方向：不建立 `HomeStore`、`storage_home`、`storage_migration`、Store ID/locator、ready/tombstone 状态、physical purge 或 `storage/home-physical-purge` active work。

## 4. 文件操作与 Sandbox mount 边界

#886 把现有文件能力收敛为 rooted POSIX operations。每次操作从已授权的类型化 root 开始，使用 root-relative path，并满足：

- root containment，拒绝 `..`、绝对路径和 symlink escape；
- 类型化 root 物化采用 no-follow traversal；操作使用 inode-pinned contained root，允许仍在 root 内的普通相对 symlink，拒绝绝对或逃逸 symlink，并校验最终对象类型；
- read-only root 写保护；
- bounded metadata/read 与 streaming large-file I/O；
- create mode、same-root rename、append、optional fsync，以及 active-operation owner fencing；
- write 在已可能修改文件后失败时返回 outcome unknown，绝不自动重试。

Workspace/API 的 durable 文件访问直接经过 authorization 和 `WorkspaceManager`，不依赖 Session compute。Agent 在 isolating Sandbox 中仍看到普通 POSIX mount；Provider 只负责把精确授权 view 挂入固定 guest coordinate。显式选择 `none` backend 仍是 trusted-host execution，不提供进程级文件系统隔离。local、Docker 和 Kubernetes 必须通过同一操作与 mount conformance。

此边界不增加 Session filesystem transport、`stella-fs`、durable Workspace transport 或 file-access compute lifecycle。Sandbox 的 containment、只读视图和最小 mount 原则仍是强制要求。

## 5. 消费者与数据分类

#888 将 Workspace API、Agent `read`/`write`/`edit`、channel upload、prompt file、share、mutable asset 及其他直接文件消费者统一迁移到：

```text
business authorization -> typed WorkspaceManager root -> rooted POSIX operation
```

调用方不能绕过该链条使用宿主绝对路径。share 发布先读取一个确定版本，再写 immutable snapshot；immutable media、snapshot、blob 与 artifact 继续由 `BlobStore`/S3 保存。

Legacy mutable asset 是否需要迁移必须按 asset 领域证据决定。允许增加 asset-specific、离线、可验证的迁移，但不得把它扩展成通用 Workspace 迁移或 catalog 协议。没有 object-only legacy 数据证据时，不增加迁移状态机。

## 6. Skill authority

#897 使 mutable Skill bytes 以确定性 POSIX root 为唯一 authority；builtin Skill 继续来自 immutable release bundle。PostgreSQL 可保留：

- Agent Skill policy；
- 经证明确实必要且范围窄的迁移 marker；
- Reflect 的 usage、ownership 或其他业务 telemetry，但不是 mutable Skill 文件镜像。

普通目录、文件和 CLI 修改默认使用普通 POSIX 语义。受管理的 API 更新可以采用同目录 temp write、必要的 `fsync`/close，再 `rename` 的安全发布方式。revision symlink、CAS 和 GC 只有在具体 Skill 领域需求独立证明“单文件 rename 不够”时才引入；它们不是通用 Workspace 模型。

#928 只负责删除残余 legacy path API 和 caller。若 #886、#888 或 #897 已消除全部残余，#928 可以合并进前序工作或取消，不需要人为保留 rollback boundary。

## 7. 多副本正确性

多副本开放前有两个相互独立、缺一不可的前置条件。

### A. Shared POSIX deployment contract

- 所有 `stellad` 副本和 Session compute 看到同一 namespace；
- backend 通过 rename、symlink、mode、locking、append、并发读写、close-to-open 和 fsync durability conformance；
- 具备目标 workload 的 benchmark 与容量结果；
- 每个副本在接收流量前检查 mount existence、identity、read/write 能力与 freshness，readiness fail closed；
- mount 断开或语义降级时停止 admission，而不是退回 replica-local filesystem。

### B. Distributed lifecycle fencing

- PostgreSQL generation/lease 是跨副本 authority；每个副本仍保留本地 lifecycle gate；
- `AgentRun` 是 Chat、agent-originated send、channel、Webhook、Scheduler、Goal 和 Delegate 的唯一 execution lease；
- 同一 Session 最多一个 running Run；running Run 固定绑定一个 executor，不接管、不续跑；
- heartbeat 以 PostgreSQL 时间和 ownership CAS 续租，失去 ownership 或期限到达即 fail closed；
- transcript、memory、source-domain、terminal 与 Run-derived Session activity write 必须在提交该写入的同一 PostgreSQL transaction 或 guarded CAS 中校验当前 `run_id + executor_boot_id + running` ownership；stale executor 不能提交 Stella durable state。`last_viewed_at` 保持独立的授权 presentation write；
- abort 持久化并与 completion 线性竞争；通知只作 wakeup，heartbeat 兜底；
- executor 丢失或 compute operation outcome unknown 时递增 compute generation，先销毁旧资源，再创建 replacement；未知外部副作用不透明重放。

Compose 和 Kubernetes journey 可以验证 A 与 B，但不能替代任何一个前置条件。

## 8. Session、channel 与事件契约

仍然有效的 runtime 决策如下：

- `SessionSandbox` 是可丢弃的 compute；一个 Session 最多一个 live compute generation。`AgentRun` 是顶层执行单元，内部 Loop/Turn 不建立第二条 lease。
- Session activity/unread watermark 是 presentation metadata，不参与 execution admission。
- lifecycle reconciler 通过 PostgreSQL CAS 处理 expired Run、heartbeat/abort、stale executor fencing 与 crash recovery；它不是 Session owner。
- agent-originated Session send 保留 `ctx_session_inbox` 作为精确 source/target/provenance receipt 和 transcript-recovery record。live admission 原子持久化或验证 receipt、创建并关联最多一个 AgentRun、投影 input；busy、无 capacity 或 admission 前取消只提交 failed/unlinked receipt，不能进入通用 FIFO。进程死亡由关联 Run 终结且不 replay；startup 只可 append/terminalize legacy 或未关联 receipt，不能创建 Run 或调用模型/工具。process-local `turnqueue` 若保留，只能提供可丢弃 fairness。
- accepted asynchronous channel input 使用 durable per-`ChatBinding` FIFO。payload/schema/size validation、过期平台附件转 immutable content-addressed media、以及 binding/principal/deployment row+byte quota 必须在 ack 前完成。稳定 source identity 才能 deduplicate；没有稳定 identity 时不按内容猜测。
- FIFO 不得自动跳过或 dead-letter poison head；transient failure 使用 bounded backoff、可观测 blocked state 和显式审计 reject。`/new` 是有序 barrier，receipt 保存 `expected_session_id` 或 binding revision 并 compare-and-rotate；删除旧 receipt authority 前先 backfill 已消费历史 command。
- `GroupRoute` 是按 group sequence 的、可过期且仅执行分类的 claim；retry 不运行 Agent/tool/Sandbox。winner 在一个 transaction 内写 responder decision 并 materialize 唯一 FIFO items；Web fan-out 对 busy responder 记录明确 rejection，其他 accepted responder 保持本地 SSE。它不复制 AgentRun execution lease。
- 每个副本只有一条 pool-external、serialized PostgreSQL control session，承担 abort/input/config wakeup、health probe 和全局 pull/WebSocket advisory lock；transaction-pooling proxy 不受支持。control session 丢失立即 cancel listeners；graceful drain 先停 listeners 再释放 leadership；通知只作 wakeup，reconnect/full scan 与 heartbeat 修复漏通知。Webhook 可由任意副本接收。
- Publisher 从 durable config、reply envelope 与加密 capability reference 重建，不依赖 leader-local registry，并且可以由 non-leader executor 发布。
- outcome-unknown command、filesystem write 或 outbound side effect不透明重试。

发起 Run 的主 SSE 保持 local。初版不增加跨副本 token relay：远端 live attach 返回可识别的 `503 + Retry-After + run_id`，客户端轮询 durable Run 状态和 transcript；仅确认没有 active Run 时返回 `204`。

Workspace API 与此 compute 生命周期正交：它不申请 AgentRun、不读取 `SandboxRef`、不建立 Session filesystem transport。

## 9. Kubernetes 拓扑与安全

Kubernetes 中所有 `stellad` replicas 与 Session Pods 使用一个共享 POSIX namespace。每个 Pod 只挂载该次执行精确授权的 root views，不能挂载 namespace 顶层。不同 principal/Agent 的 mount view 必须互不越界；read-only Skill source 必须在 mount 层只读。

调度 compute 与定位 durable bytes 相互独立；Pod replacement 或跨节点调度始终重新挂载同一确定性 namespace。

Session Pod 安全基线：

- 不自动挂载 ServiceAccount token；
- non-root、drop all capabilities、seccomp，并按平台使用 AppArmor/SELinux；
- 禁止 privileged、host namespaces、hostPath 与 Docker socket；
- 默认拒绝 egress，只开放明确目标；
- 配置 CPU、memory、PID 和 ephemeral-storage limits；
- `stellad` 与 Session Pod 使用分离的 RBAC 和网络边界。

Kubernetes readiness 必须同时证明 shared-POSIX mount contract 和 distributed lifecycle fencing。单纯成功创建 Pod 或成功运行 Compose 不能开放多副本。

## 10. Conformance 与验收

### Workspace

- typed deterministic roots 幂等；user/group raw-ID collision 隔离；
- durable owner 查询失败时零文件系统变更；
- root replacement、symlink、wrong-kind 和 traversal fail closed；
- owner deletion 与 concurrent admission 按 gate 线性化，并保留 bytes/inodes；
- filesystem occupancy 阻止 Agent-ID reattachment。

### Rooted operations and mounts

- 所有操作均无法逃逸授权 root；read-only view 不可写；
- local 与 shared backend 对 metadata、rename、locking、concurrency 和 durability 行为一致；
- disconnect/kill 的错误分类不会触发透明重试；
- Workspace/API 无 Session、无 warm Sandbox 时仍可直接访问 durable root；
- Sandbox 仅看到 exact authorized mount views。

### Multi-replica

- A 与 B 两项 prerequisites 分别有配置校验、conformance 和 failure tests；
- 两个副本不能为同一 Session 同时执行 AgentRun；
- heartbeat loss、executor death、abort、replacement 与 stale writes 均被 generation/lease fence；
- replacement admission 后，stale executor 的 transcript、memory、source-domain、terminal 和 Run-derived Session activity transaction/CAS 全部失败；
- Workspace operation 可与另一副本的 AgentRun 按普通 POSIX 语义并发；
- channel attachment 在平台 URL 过期后仍可从 immutable media ref 读取；quota 在 ack 前执行；poison head/barrier 不被跳过；历史 `/new` redelivery 不旋转 successor；
- stale `GroupRoute` claimant 不能提交，responder materialization 保持原子，partial-busy fan-out 不丢 accepted responder；
- control-session loss 会停止旧 listeners，handoff 顺序正确，漏通知由 scan/heartbeat 修复，transaction pooling fail closed；
- channel FIFO、GroupRoute、Publisher reconstruction 和 ingress leader failover 不依赖 process-local correctness，non-leader replica 可以发布；
- local SSE 与 remote polling fallback 保持 durable terminal result。

## 11. 明确拒绝或已废弃

以下名词只描述被拒绝/已废弃的方向，不是 active work：`HomeStore`、`HomeAttachment`、`storage_home`、`storage_migration`、Store ID/locator、ready/tombstone、physical purge、`storage/home-physical-purge`、durable-workspace `stella-fs`、exact-Session filesystem RPC、per-Agent RWO PVC/affinity/trusted provisioner。

同样不做 persistent VM、Workspace object checkout/merge、透明 replay、跨副本 token broker、mixed-version rolling execution 或内建 backup/restore 产品。Immutable `BlobStore`/S3 明确保留。

## 12. 被取代的历史

早期 Fable/Sol review 曾围绕 catalog、provider attachment、physical cleanup、Skill revision tree 和固定 PR 拆分批准旧稿。这些 review 只说明当时文本经过审阅；本次重写已经取代其架构结论，不能引用为当前方案的批准证据。当前依据是本文及其当前实施计划，旧细节留在 Git 历史中。
