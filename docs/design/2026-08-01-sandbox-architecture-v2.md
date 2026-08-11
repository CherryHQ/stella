# Sandbox 架构 v2：可迁移 Home 与可重连 Session Sandbox

- **日期**：2026-08-01（rev 8；2026-08-10 对齐 PR #940、#962）
- **状态**：设计已批准；实施由 CherryHQ/stella#828 跟踪
- **关联 Issue**：CherryHQ/stella#828（实施跟踪）
- **关联 PR**：CherryHQ/stella#829（Draft，未合并）
- **实施计划**：`docs/design/2026-08-02-sandbox-architecture-v2-implementation-plan.md`（Fable approved）
- **Phase 0 计划**：`docs/design/2026-08-02-system-skill-bundle-phase-0-plan.md`（Fable approved）
- **范围**：持久目录、Sandbox Provider、Session 执行所有权、Docker 与 Kubernetes 多副本、文件访问边界、故障恢复和迁移路径
- **相关设计**：`docs/design/2026-07-04-sandbox-secrets-injection.md`、`docs/design/research/2026-07-04-k8s-multi-replica-readiness.md`

本文替代 rev 2。rev 2 把持久 Machine、独立 Worker 和 sessionless 直读 Workspace Store 当作目标形态；后续讨论已经否定这些前提。当前设计从产品要保留的数据和故障语义出发，不延伸现有 `STELLA_HOME` 路径布局。

rev 8 把 Skill current state 收敛为 release bundle 与 Home filesystem 两个 authority，删除原计划的 PG→Session scratch 终态。Fable 对 rev 3 Skill 方案的 round 1 提出 policy clobber、legacy array、Reflect、原子目录替换、legacy filesystem system Skill 和 PG-only metadata 六项必改；全部关闭后，round 2 明确 `APPROVED`，无剩余 mandatory finding。

## 1. 已确认的决策

| 编号 | 决策                                                                                                                                                                                                                                                                                                                                                                                                          |
| ---- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| D1   | “个人电脑”描述持久用户目录的体验，不代表一台长期运行的个人虚拟机。Sandbox 的 rootfs mutation、进程、socket、网络状态和机器身份都可丢弃。                                                                                                                                                                                                                                                                      |
| D2   | user 与 group 都是显式 principal，各有跨其所有 Agent 共享的 `PrincipalHome`；specialization 分别叫 `UserHome` 与 `GroupHome`。每个 `(principal_kind, principal_id, agent_id)` 有隔离 `AgentHome`；HomeStore 另管理窄化的共享 `SystemSkillRoot` 与 `SystemAgentSkillRoot(agent_id)`；user-less Session 没有 writable persistent Home。                                                                         |
| D3   | 同一 principal 的不同 Agent 必须能在不同节点并发运行。同一 AgentPlacementKey 的多个 Session 无需跨节点，允许在同一节点并发运行。                                                                                                                                                                                                                                                                              |
| D4   | `PrincipalHome` 和 `AgentHome` 上的并发写遵循普通共享 POSIX 文件系统语义。Stella 不做 MVCC、版本合并、全局写锁或透明冲突修复。                                                                                                                                                                                                                                                                                |
| D5   | Kubernetes 中一个活跃 Session 对应一个可保温的普通 OCI Pod。Pod 空闲后删除；只有 Home 数据继续存在。                                                                                                                                                                                                                                                                                                          |
| D6   | 可信 Agent Loop 留在 homogeneous `stellad` 副本中。初版不拆 Executor Fleet，也不建立 Stella 自己的 VM 调度器。                                                                                                                                                                                                                                                                                                |
| D7   | `SandboxProvider` 是完整策略的 tagged union。初始 Provider 只有 `local`、`docker`、unsafe-gated `none` 和普通容器 `kubernetes`。smolvm、Kata、OpenSandbox 和通用 Remote Provider 延后。                                                                                                                                                                                                                       |
| D8   | Provider 的分布式能力由当前配置能否持久定位并重连资源决定，不按 Provider 名称写死。多个 `stellad` 连接同一个 Docker daemon 时，Docker Provider 可以支持多副本。                                                                                                                                                                                                                                               |
| D9   | Session 拥有唯一的逻辑 SessionSandbox，Sandbox 不绑定任何 `stellad`。支持分布式重连的 Provider 允许任意副本 `Open` 同一有效 generation；副本只在单个 Agent Run 期间临时执行 Loop。                                                                                                                                                                                                                            |
| D10  | Agent Run 非正常失去 executor 或 Sandbox 操作 outcome unknown 后，必须递增 generation，销毁旧 Pod/Container，再创建新资源。后续 executor 不复用可能仍有旧操作的计算环境。                                                                                                                                                                                                                                     |
| D11  | `PrincipalHome` 在 Kubernetes 中使用共享 RWX Pool 加隔离 `subPath`，不为每个 user/group 创建一个 PVC。每个 Pod 只挂载该 principal 目录和适用的只读共享 Skill root，不挂载 Pool 根。                                                                                                                                                                                                                           |
| D12  | `AgentHome` 在 Kubernetes 中按 `(principal_kind, principal_id, agent_id)` lazy provision 一个独立 RWO PVC。多个 Session Pod 通过 required Pod affinity 共置到同一 hostname。                                                                                                                                                                                                                                  |
| D13  | PostgreSQL 的 `storage_home` registry 保存跨 Provider 的逻辑 Home/共享 Skill root 身份和生命周期。`HomeStore` adapter 决定 local 目录、Docker volume/subpath 或 Kubernetes PVC/subpath。                                                                                                                                                                                                                      |
| D14  | Store identity 一旦被 Home 引用就不可原地改变。修改默认 Store 只影响新 Home；已有 Home 使用显式、停机式迁移，不做在线双写。                                                                                                                                                                                                                                                                                   |
| D15  | Home 删除采用 `tombstone -> asynchronous purge`。River 执行幂等物理删除；Session 归档、principal 与 Agent 解除分配、Helm uninstall 都不删除 Home。                                                                                                                                                                                                                                                            |
| D16  | 当前范围不实现备份、快照或恢复 API。operator 负责备份 PostgreSQL、RWX Pool 和 AgentHome PVC。                                                                                                                                                                                                                                                                                                                 |
| D17  | `ResolvePath` 和 `ResolveWritePath` 从跨 Provider 契约删除。远程文件操作通过 Provider 原生 exec 启动一次性 `stella-fs` helper；不部署常驻 sidecar。                                                                                                                                                                                                                                                           |
| D18  | Stella 不把 S3 object API 实现成主文件系统，也不让普通命令经过 object checkout/merge。S3 可保存 immutable blob，或作为通过 POSIX conformance 的外部文件系统之内部介质。                                                                                                                                                                                                                                       |
| D19  | Agent cache 随 `AgentHome` 持久化但可回收；Worker `NodeCAS` 是可选优化。Session `/tmp`、socket 和进程状态始终是临时数据。                                                                                                                                                                                                                                                                                     |
| D20  | 任意 exec/write 在连接中断时都不得透明重试。当前 Agent Run 标记为 interrupted，副作用结果标记为 unknown。                                                                                                                                                                                                                                                                                                     |
| D21  | Workspace API 由收到请求的副本对 URL 中精确 `sessionID` 的当前 generation 执行 `BeginUse + Open`，复用或唤醒该 Session 唯一的 Sandbox，并且只允许执行 `stella-fs`。初版不新增 file-access Pod。                                                                                                                                                                                                               |
| D22  | Web/API/Webhook 在 Session busy 或已有排队输入时 fail fast；异步 channel 输入持久 FIFO。当前 Run 结束后，系统把已排队的有界、连续、兼容输入原子合成一个新 Agent Run，不逐条启动 Run。                                                                                                                                                                                                                         |
| D23  | batch 只合并 execution envelope 完全相同的队首连续输入，包括 authority、reply/ChatBinding、model/system/tool policy 和 run kind。遇到不兼容输入或 control barrier 立即截断，不跳过或重排。                                                                                                                                                                                                                    |
| D24  | `AgentRun` 是 Chat、agent-originated Session send、Channel batch、Webhook、Scheduler、Goal 和 Delegate 共用的唯一 execution lease。Session unread/activity watermark 只记录持久 presentation metadata，不参与 admission。初始 lease 90 秒、每 20 秒 heartbeat；续租失败必须不晚于保守 deadline fail closed。                                                                                                  |
| D25  | `/abort` 通过 `abort_requested_at` CAS 持久化；事务内 PostgreSQL `NOTIFY` 只负责唤醒 executor，heartbeat 负责兜底。abort 与 completion 线性竞争，不清除 queued input。                                                                                                                                                                                                                                        |
| D26  | 初版不实现跨副本 token-level event relay。发起 Run 的 SSE 保持直连；远端 read-only attach 返回 `503 + Retry-After`，Web 轮询 durable Run 状态与 transcript，不能误报 `204`。                                                                                                                                                                                                                                  |
| D27  | ingress 只按来源提供的稳定 identity 去重；duplicate 返回原 receipt/Run 状态，不再次排队或执行。没有稳定 identity 时每次 delivery 都视为新输入，禁止用 body hash 猜测去重。                                                                                                                                                                                                                                    |
| D28  | AgentRun 不设固定 lease safety margin。以 PostgreSQL 返回的剩余期限和请求开始时刻计算保守 monotonic deadline；deadline 与每次操作前检查负责软停止，hard fencing 负责最终互斥。                                                                                                                                                                                                                                |
| D29  | v2 保持现有 Vault/OAuth env、refresh、redaction 和 group isolation 语义，不并入 secrets redesign。Kubernetes 用一次性 `stella-exec` 从 stdin 注入当前 env，不把值写入 PodSpec 或持久 ref。                                                                                                                                                                                                                    |
| D30  | PrincipalHome/AgentHome 的逻辑 authority 是 POSIX filesystem namespace，物理实现可以在内部使用 S3，但必须通过 conformance。mutable asset 是 PrincipalHome 文件，不再镜像到另一份 object authority。                                                                                                                                                                                                           |
| D31  | 第一方 Web/API 只使用 `client_message_id` 做幂等，不增加 header 机制。Web 必须发送稳定 UUID；兼容期 API 可省略。duplicate 返回带原 ingress/Run 状态的结构化 `409`，不重放 SSE。                                                                                                                                                                                                                               |
| D32  | pull/WebSocket channel 共用一个 PostgreSQL session advisory-lock ingress leader；Webhook 保持 stateless。Publisher 从 durable config 在任意副本构建，不把 AgentRun 路由回 leader。                                                                                                                                                                                                                            |
| D33  | 每个副本只保留一条 pool 外 PostgreSQL control session，串行承担 abort/input/config `LISTEN`、健康检查和 channel advisory lock；不为每个 channel 或每种控制信号占一条 connection。                                                                                                                                                                                                                             |
| D34  | AgentRun 没有 queued 状态、claim token 或 AgentLoop 表。来源领域可持有 receipt、恢复或排队状态，但不能持有另一条 running lease；executor 真正开始时创建 running Run，running Run 永不转交，超时后 interrupted，新执行使用新 `run_id`。                                                                                                                                                                        |
| D35  | queued channel input 不冻结收件时的 authority 或 model/system/tool policy。dispatcher 在 admission 时重新授权并解析当前 policy；明确撤权进入 rejected，临时错误保持 queued。                                                                                                                                                                                                                                  |
| D36  | v2 移除 channel `/agent` 命令。Agent 与稳定 ChatBinding 在收件时确定；管理员改路由只影响提交后新 ingress，因此 channel ordering 只需一层 per-ChatBinding durable FIFO。                                                                                                                                                                                                                                       |
| D37  | 保留 group semantic auto-routing，但只保留一个 durable `GroupRoute` claim。routing decision 原子生成 ChatBinding FIFO items；删除 `ctx_group_dispatch` 的第二套执行 lease，由 AgentRun 执行。                                                                                                                                                                                                                 |
| D38  | Home 只有显式 destructive delete 才 tombstone 并立即异步 purge；不增加 retention 或 undelete。物理数据删除后永久保留 purged metadata，失败时由管理员 retry。                                                                                                                                                                                                                                                  |
| D39  | Kubernetes PrincipalHome 初版使用一个显式配置的 RWX Pool。StorageClass/existing claim 与 capacity 无默认猜测，启用前必须通过 POSIX conformance；不做自动扩容或分片。                                                                                                                                                                                                                                          |
| D40  | AgentHome 的 RWO StorageClass 与初始容量同样由 operator 必填；只允许显式扩容，不自动缩放。attach limit 和总存储配额分别交给 scheduler/CSI 与 namespace ResourceQuota。                                                                                                                                                                                                                                        |
| D41  | Skill current state 只有两个 authority：release-owned builtin 使用 digest-pinned immutable system bundle；`system`、`system_agent`、`user`、`user_agent` 与 `project` 都是 Home filesystem 文件。PostgreSQL SkillStore 只在离线 cutover 前临时保留，之后不再物化 DB skill scratch。                                                                                                                           |
| D42  | 通用 ChatBinding receipt/FIFO 只新增 `ctx_chat_input`；group payload 引用现有 `ctx_group_message`，其他通用来源由 input 自带。PR #962 已有的 `ctx_session_inbox` 保留为 agent-originated Session send 的来源领域 receipt 与 transcript-only recovery record，不并入 ChatBinding FIFO，也不承担 running lease；两者都必须通过 AgentRun admission。广播 `NOTIFY` 与 safety scan 只服务需要异步排队的通用 lane。 |
| D43  | GroupRoute 按 group message seq 串行 materialize。Web requester 持有 route claim 时必须在本副本直接 admission selected Runs；容量或 Session busy 就 fail fast，不能让广播竞争偷走主 SSE。                                                                                                                                                                                                                     |
| D44  | stale executor 的 PostgreSQL/transcript 写入必须以 `run_id + executor_boot_id + running` CAS；Sandbox 删除不能 fence 外部 API，未知 outbound side effect 只记 unknown 且不得自动重放。                                                                                                                                                                                                                        |
| D45  | `/new` input 在 receipt 时保存 `expected_session_id` 或 binding revision，并以 compare-and-rotate 处理；只有普通 message 在 admission 时解析 binding 当前 Session。                                                                                                                                                                                                                                           |
| D46  | channel cursor/ack 只能在 durable ingress commit 后推进；reply capability 等 secret 状态进入现有 encrypted Vault，`reply_envelope` 只保存 reference，任意 Publisher 副本不依赖 leader memory。                                                                                                                                                                                                                |
| D47  | Kubernetes RWX Pool root 只由 HomeStore 创建的一次性 trusted provisioner Pod 挂载，用于 Ensure/Purge/conformance 与结构化、认证后的 admin Skill 写入；它不接收模型命令/任意代码，不挂进 `stellad`，也不创建常驻 sidecar/controller。                                                                                                                                                                          |
| D48  | v2 移除整个 channel `/model` 命令，不增加只读替代命令或 per-ChatBinding model override。Agent model 只通过有明确作用域的 Web/API 配置；删除无作用域且当前实际 no-op 的 `SwitchModel`。                                                                                                                                                                                                                        |
| D49  | 多副本前把 connection OAuth flow 与登录/注册 rate limit 移到 PostgreSQL；flow secret 加密且 one-shot CAS。config mutation 发 versioned `NOTIFY`，但授权/admission 不依赖通知命中。                                                                                                                                                                                                                            |
| D50  | 初版不支持 mixed-version `stellad` 并发。Helm 保持 drain 后 `Recreate`，先停完旧副本再启动同版本新副本；接受升级窗口，不为本设计增加 dual-read/write rolling protocol。                                                                                                                                                                                                                                       |
| D51  | 现有 `users/group-{groupID}` 迁移为 `GroupHome(groupID)`，保留 group workspace/assets；group AgentRun 不挂任何成员的 UserHome，初版也不把 GroupHome/group AgentHome 的 `.agents/skills` 解释成 `user`/`user_agent` scope。group 与 user-less Run 只读挂适用的 system/system_agent roots，再使用自己的 project/scratch。                                                                                       |
| D52  | 每个副本运行同一个轻量 Sandbox lifecycle reconciler。它以 PostgreSQL CAS 回收 expired AgentRun、fence generation，并续行 `fencing`、provisioning orphan 与 idle Sandbox；不是 Session owner 或第二套 lease。                                                                                                                                                                                                  |
| D53  | Web group fan-out 按 responder fail fast：busy/no-capacity responder 的 input 持久化为 `state=rejected, reject_code=busy` 且不排队；其他 responder 在请求副本直接 admission 并保持 SSE。全部拒绝时整体返回 busy。                                                                                                                                                                                             |
| D54  | Kubernetes 前必须先通过 Docker Compose 多副本硬门禁：共享 external PostgreSQL、Docker daemon 与 reconnectable Home，验证跨副本协议和故障恢复。Kubernetes 阶段只再承担 Pod/PVC/CSI/topology/RBAC/network-policy 差异。                                                                                                                                                                                         |
| D55  | 删除 mutable asset object authority 前，必须离线把全部 object-only asset 物化并校验到对应 PrincipalHome，记录 migration marker；配置过旧 authority 但未完成迁移时 server 启动失败，禁止静默丢数据。                                                                                                                                                                                                           |
| D56  | 现有 `agent.enabled_builtin_skills` JSONB 复用为 versioned `AgentSkillPolicy`；legacy array 一律保持当前“无禁用”行为，非空仅告警。policy 只引用 `builtin:<name>`、`system:<name>`、`system_agent:<name>`；admin 与 Agent creator 共用同一 per-Agent 设置，不新增 activation table 或全局 hard-lock。                                                                                                          |
| D57  | mutable Skill 的 scope 由独立 filesystem root 决定；scope+name 是不可 rename 的逻辑身份。优先级保持 `project > user_agent > user > system_agent > system > builtin`；禁用 winner 不回退到同名低层 Skill。                                                                                                                                                                                                     |
| D58  | managed Skill create/update 使用 immutable revision directory 加受限相对 symlink 原子 flip；普通目录与任意 CLI 编辑保留普通 POSIX 语义。API 不伪造“rename 覆盖非空目录”的事务原子性，旧 revision 在 AgentRun reference-aware GC 前不删除。                                                                                                                                                                    |
| D59  | PG→Home Skill authority cutover 在 typed Homes 与 `stella-fs` 落地后、AgentRun/多副本前停机执行。marker 前 PG 是唯一 authority，marker 后 Home 是唯一 authority；不做 dual-write、长期 dual-read 或失败 fallback。                                                                                                                                                                                            |
| D60  | Reflect Skill 保留，但内容 authority 迁到 `user_agent` filesystem root：tree digest 取代 PG version CAS，`created_by=reflect` 进入规范 metadata，usage 改按逻辑身份+digest 保存为派生 telemetry；PG changelog 退役。任意 POSIX writer 的竞争遵循 D4。                                                                                                                                                         |

## 2. 当前实现的问题

当前实现把“`stellad` 能看到 Sandbox 的宿主路径”写进了多层契约：

1. `pkg/sandbox/session.go` 暴露 `ResolvePath` 和 `ResolveWritePath`，并要求调用方对返回值使用 `os.*`。
2. `internal/agent/sandbox/read.go`、`write.go` 和 `edit.go` 在 `stellad` 进程中直接读写 Sandbox 文件。
3. `internal/agent/session/access/workspace.go` 在没有 Session 时从宿主路径直接浏览和修改 Workspace。
4. `pkg/sandbox.Mount` 使用 `HostPath` 表达数据身份；local、none 和当前 Docker 实现都依赖 daemon 或进程可见的宿主路径。
5. `agent.workspace` 和 `project.base_dir` 保存机器坐标。`internal/agent/project_store.go` 从 `STELLA_HOME` 推导用户与 Agent 路径。
6. runner cache、active-turn guard、SSE hub、channel FIFO 和 `internal/agent/session/turnqueue` 都是进程内状态。请求落到另一个副本时，它们不能提供跨副本排他、序列化或事件转发。
7. Docker Session 只在内存保存 Container ID、policy 和 mount table。启动清理使用 `owner_pid` 判断孤儿；PID 在容器 namespace 或远程 daemon 场景下不能表示分布式所有权。
8. Helm 当前固定一个副本、`Recreate` 和一个 RWO PVC，不能承载目标拓扑。
9. `$STELLA_ASSETS_DIR` 可被 guest 直接写入，绕过 `asset.Store`。`internal/asset/store.go` 也缺少跨副本对象版本 fencing。
10. channel `/model` 宣称可以切换模型，但 server 注入的 `SwitchModel` 永远 no-op，接口也没有 principal、Agent 或 ChatBinding scope；继续保留会制造虚假成功和未定义的跨用户影响。
11. Skill 同时存在 release embed、PG `skill/skill_file`、启动抽取目录和 project filesystem 多套来源；PG→disk materializer 与 filesystem scan 让“目录是 authority 还是 cache”取决于 scope，脚本 mode、Reflect lifecycle 和多副本重建因此耦合到旧宿主路径。
12. PR #940 持久化了 Session 的 turn activity/viewed watermark，但 `running` 仍由本进程 runtime 判断；PR #962 为 agent-originated `session.send` 增加 `ctx_session_inbox`、启动时 transcript-only recovery 和进程内 `turnqueue`。这些是当前单副本产品语义，不是跨副本 execution lease。

旧稿声称 `read.go` 会在宿主调用 xberg，这与当前代码不符。当前 host-side xberg 位于 `internal/vision/xberg.go`。v2 仍需审计所有解析不可信文件的宿主消费者，但不能用已经不存在的调用链证明问题。

## 3. 领域模型

### 3.1 七个核心名词

| 名词             | 身份                                    | 生命周期                           | 内容                                                                                |
| ---------------- | --------------------------------------- | ---------------------------------- | ----------------------------------------------------------------------------------- |
| `PrincipalHome`  | `(principalKind, principalID)`          | 与 user/group 数据同寿命           | principal 级配置、共享文件、assets 和安装工具；specialization 为 UserHome/GroupHome |
| `AgentHome`      | `(principalKind, principalID, agentID)` | 与该 principal 的 Agent 数据同寿命 | workspace、project 文件、Agent 私有配置和可回收 cache                               |
| `Session`        | `sessionID`                             | 持久，可归档                       | 对话历史、授权关系和 Session metadata                                               |
| `SessionSandbox` | `sessionID + generation`                | 短命，可保温，空闲回收             | 该 Session 唯一的活跃计算环境；rootfs、进程、env、network 和 `/tmp`                 |
| `AgentRun`       | `runID`                                 | 一次顶层 execution unit            | Session 排他、executor lease、abort 和故障 fencing；可包含一个或多个 Loop           |
| `AgentLoop`      | `runID + loopOrdinal`                   | 一次模型驱动循环                   | 普通输入通常一个；Goal repair 可在同一 Run 内执行额外的有界 Loop                    |
| `Turn`           | `runID + loopOrdinal + ordinal`         | 一次模型/工具循环                  | 一次模型调用及其产生的工具调用；不是排他或故障接管边界                              |

一个 Session 可以顺序执行多个 Agent Run；一个 Agent Run 可以执行一个或多个 AgentLoop，每个 Loop 又包含多个 Turn。只有正在执行的 Agent Run 临时位于某个 `stellad` 副本，Session 和 Sandbox 都不归该副本所有。

“Agent Runtime”只可用作逻辑分组名称，不能表示一个长期占有 Agent 或 Session 的 `stellad` owner。需要精确表达时，使用下面四个 key：

```text
AgentPlacementKey    = (principal_kind, principal_id, agent_id)
SandboxLogicalKey    = session_id
SandboxGenerationKey = (session_id, generation)
RunExclusionKey      = session_id
```

`AgentPlacementKey` 只在 Session 有 typed principal/AgentHome 时存在；user-less Session 没有 RWO placement constraint，可以按普通 disposable Pod 调度。

### 3.2 持久化边界

| 数据                                      | 故障或迁移后是否保留   |
| ----------------------------------------- | ---------------------- |
| PrincipalHome 文件                        | 保留                   |
| AgentHome 文件                            | 保留                   |
| SystemSkillRoot/SystemAgentSkillRoot 文件 | 保留                   |
| AgentHome 中的可回收 cache                | 通常保留，允许 GC 删除 |
| Session rootfs mutation                   | 不保留                 |
| 运行中的进程与 shell                      | 不保留                 |
| Unix socket、端口与 network namespace     | 不保留                 |
| `/tmp` 与 Session scratch                 | 不保留                 |
| 当前命令的“是否已经产生副作用”            | 连接中断后记为 unknown |

不同 Session 只共享显式挂载的 Home。它们各自拥有进程、环境变量、网络状态、scratch 和 rootfs overlay。

## 4. 目标拓扑

```text
           shared PostgreSQL
      active run claim / sandbox ref
                /       \
    stellad replica A   stellad replica B
      Run R1 on S1       Run R2 on S2
    trusted Agent Loop  trusted Agent Loop
           |             /      |
           +------------+       |
           |                     |
      S1 Pod/Container      S2 Pod/Container
      |          |          |          |
PrincipalHome RWX  AgentHome RWO  ...      /tmp
      \       read-only shared Skill roots      /
```

所有 `stellad` 副本使用同一二进制和同一角色。收到 Agent Run 的副本对 `sessionID` 获取短期排他 claim，运行完整 Agent Loop，然后释放；下一个 Run 可以落到其他副本。收到 Workspace 请求的副本对当前 generation 执行 `BeginUse + Open`，即使 Agent Run 正在另一个副本执行也不需要转发。runner 和 Sandbox handle 可以缓存，但缓存命中不参与正确性。

同一 Session 最多有一个 live Sandbox generation，但支持分布式重连的任意副本都能从 PostgreSQL 中持久的 ref 重建 handle。初版不需要 Session owner、owner routing 或专用 Executor；`/abort` 使用 PostgreSQL 持久请求和通知，live event 不建立跨副本数据面。

分布式部署要求共享的外部 PostgreSQL。embedded PostgreSQL 仍服务单机部署，不进入多副本拓扑。

## 5. Home 与存储

### 5.1 逻辑目录

每个 user 或 group principal 的所有 Agent 共享一个 PrincipalHome；每个 Agent 只挂载该 principal 下自己的 AgentHome：

```text
User U
├── UserHome(user, U)              shared by A1 and A2
├── AgentHome(user, U, A1)         isolated from A2
└── AgentHome(user, U, A2)         isolated from A1

Group G
├── GroupHome(group, G)            shared by A1 and A2
├── AgentHome(group, G, A1)        isolated from A2
└── AgentHome(group, G, A2)        isolated from A1

Shared admin-managed Skills
├── SystemSkillRoot                 visible to applicable Runs
└── SystemAgentSkillRoot(A1)        visible only with Agent A1
```

GroupHome 继承当前 `users/group-{groupID}` 的产品语义，但 namespace 使用 typed principal identity，不再靠字符串前缀防碰撞。group Session 只能挂自己的 GroupHome、group-scoped AgentHome 和适用的只读 SystemSkillRoot/SystemAgentSkillRoot，不能挂任一成员的 UserHome，也不能把成员的 Vault/OAuth/CLI credential 复制进 group filesystem。初版不把 GroupHome 或 group AgentHome 的 `.agents/skills` 解释为 `user`/`user_agent` scope。user-less Session 不创建 PrincipalHome 或 AgentHome，只挂适用的只读共享 Skill roots、DB-derived Agent definition 和 disposable Session scratch；当前 `{base}/agents/{agentID}` 不能继续充当 writable workspace 或 Skill authority。

mutable Skill 的逻辑 root 与内容 authority 如下一一对应；实际子目录名由 HomeStore 隐藏，不能由调用方拼接：

```text
system       -> SystemSkillRoot
system_agent -> SystemAgentSkillRoot(agent_id)
user         -> UserSkillRoot(UserHome)
user_agent   -> UserAgentSkillRoot(AgentHome)
project      -> ProjectRoot/.agents/skills
```

`UserAgentSkillRoot` 与 `ProjectRoot/.agents/skills` 必须是两个不重叠 attachment，即使 ProjectRoot 位于 AgentHome 内也不能 alias。scope 由 root 决定，不由可伪造的 frontmatter 字段决定。

### 5.2 各 Provider 的物理实现

| 环境          | PrincipalHome                                | AgentHome                                                  |
| ------------- | -------------------------------------------- | ---------------------------------------------------------- |
| local / none  | typed user/group Pool 隔离目录               | 本地独立目录                                               |
| Docker bind   | daemon 可见 Pool 的 typed 隔离目录           | daemon 可见独立目录                                        |
| Docker volume | shared named volume + typed opaque subpath   | 同一 daemon 放置域中的 volume/subpath                      |
| Kubernetes    | RWX Pool PVC + typed opaque isolated subPath | 每 `(principal_kind, principal_id, agent_id)` 一个 RWO PVC |

逻辑 Home 不等于一个 Kubernetes PVC。PrincipalHome 的物理 Pool 可以在达到吞吐或容量上限后分片；`storage_home` 中的 opaque locator 隐藏这一点。

SystemSkillRoot 与 SystemAgentSkillRoot 在所有环境都使用 PrincipalHome 同类的共享 RWX Store/volume，而不是 RWO AgentHome。它们必须被多个 principal、Session 和副本同时只读挂载；把 system_agent 内容放进任一 principal 的 AgentHome 会泄漏 ownership，也无法服务其他 principal。

### 5.3 `storage_home` registry

PostgreSQL 保存 Home 的身份、归属、Store 和生命周期。下面是领域字段，不是最终 migration：

```text
storage_home
  id             UUID
  home_kind      principal | agent | system_skill | system_agent_skill
  principal_kind user | group | nullable
  principal_id   nullable
  agent_id       nullable
  store_id
  locator        opaque
  state          provisioning | ready | tombstoned
                 (future provider-fenced purge adds purge_failed | purged)
  created_at
  updated_at
```

结构约束：

```text
PrincipalHome unique(principal_kind, principal_id)
              where home_kind = principal
AgentHome     unique(principal_kind, principal_id, agent_id)
              where home_kind = agent
SystemSkillRoot unique(home_kind)
                where home_kind = system_skill
SystemAgentSkillRoot unique(agent_id)
                     where home_kind = system_agent_skill

check(home_kind = principal
      AND principal_kind IS NOT NULL AND principal_id IS NOT NULL AND agent_id IS NULL
   OR home_kind = agent
      AND principal_kind IS NOT NULL AND principal_id IS NOT NULL AND agent_id IS NOT NULL
   OR home_kind = system_skill
      AND principal_kind IS NULL AND principal_id IS NULL AND agent_id IS NULL
   OR home_kind = system_agent_skill
      AND principal_kind IS NULL AND principal_id IS NULL AND agent_id IS NOT NULL)
```

`system_skill` 全部署唯一，`system_agent_skill` 对 `agent_id` 唯一；两者使用与 PrincipalHome 相同的显式 RWX Store，但拥有独立 opaque locator。它们是窄化的共享 Skill namespace，不是可写 global Agent workspace。删除 Agent 时，只有明确 destructive Agent delete 才 tombstone 对应 SystemAgentSkillRoot；普通 assignment/session 变化不影响它。

`auth_user_agent` 和 group membership 只表示授权关系，不能承载 Home 生命周期。解除分配不删除文件。全局 `agent.workspace` 也不能表示任一 typed principal 的 AgentHome。

`HomeStore` 初版只提供四项能力：

```text
Ensure(home)      create physical storage idempotently
Resolve(home)     return a provider-compatible HomeAttachment
Tombstone(home)   revoke future attachment
Purge(home)       delete physical storage idempotently
```

Phase 1 的可合并子集只实现 `Ensure`、ready-root inspection、`Resolve` 与 `Tombstone`，并永久保留 tombstoned identity/locator 与物理字节。`Purge` 是本节最终架构能力；它在 provider/filesystem 边界之后的独立 Draft 中实现，不能由 Phase 1 的 host-path 兼容层提前执行。

Phase 1 的单进程删除 fence 使用一个 process-wide、writer-progress 的 shared/exclusive lifecycle gate。所有真实 Turn admission 仅在同步选择或构造 runner（包括 `WorkspaceView`）期间持 shared；Service 发布、`SyncAgent` reconcile、remove、shutdown 与 owner deletion 持 exclusive。owner deletion 的固定顺序为 lifecycle exclusive → Home process owner gate → PostgreSQL advisory/row locks，并把 exclusive 保留到 owner transaction commit/rollback。Runtime close 可以让这个低频 writer 变慢，但 writer 不等待 active Turn 完成，也不得在 exclusive 内调用 `WaitTurns`。

这只解决当前 single-replica 的进程内正确性，不能伪装成 distributed fence。未来 multi-replica 必须把 PostgreSQL generation/lease 与每个副本的 process gate 组合。durable management write 后 best-effort `SyncAgent` 若失败，runtime 仍可能暂时 drift，直到下一次 reconcile；plugin hook reload 对已 active Turn 的旧 generation 也仍缺少 reference-counted retirement，本 gate 不声称解决这两项问题。

`SandboxProvider` 只接收 `HomeAttachment`。它不决定 Home 放在哪里，也不把 compute Provider 名称写进数据身份。

### 5.4 Store 修改与迁移

Store identity 被 Home 引用后保持不变。允许的配置变更只有：

- 增加一个 Store；
- 修改新 Home 使用的默认 Store；
- 轮换仍指向同一物理存储的凭据和证书。

改变 root、PVC、volume 或后端服务意味着迁移，不能沿用原 `store_id`。显式迁移按以下顺序执行：

1. 获取 Principal 或 Agent maintenance lock。
2. 停止并 fence 相关 Session。
3. 复制文件并保留 POSIX metadata。
4. 校验文件数量、大小和内容。
5. 在 PostgreSQL 中 CAS 切换 `store_id + locator`。
6. 恢复 Session。
7. 保留旧副本作为短期回滚材料，之后显式 purge。

初版不做在线双写迁移。改变 SandboxProvider 时，如果新 Provider 能挂载当前 Store，就不迁移数据；否则先单独迁移 Home。

### 5.5 删除

PostgreSQL 和 CSI、Docker volume 或宿主文件系统不能参与同一个事务。Home 删除使用两阶段流程：

1. 数据库将 Home 标记为 `tombstoned`。
2. 新 Session 不再获得该 Home 的 attachment。
3. 系统 fence 仍在使用该 Home 的 Session。
4. 等待引用该 Home 的 Pod/Container 全部不存在；独占 PVC 场景还要确认 detach。
5. River job 幂等执行物理 purge。
6. 物理删除失败时保留 registry 记录并重试。

删除或归档 Session 不影响 Home。解除 user/group 与 Agent 的分配也不影响 AgentHome。只有经过明确 destructive confirmation 的 Agent、User 或 Group 删除会 tombstone 相关 typed Home，并立即投递异步 purge；当前不增加 retention、undelete 或 restore product。物理数据删除后，registry 进入 `purged` 并永久保留 identity、ownership 与审计 metadata；不得复用该 Home ID 或 locator。`purge_failed` 只允许管理员幂等 retry，不能重新 attachment。

PVC 和目录不能把 Session Pod 设为 owner。Helm uninstall 不触发 purge，Kubernetes PV 的 reclaim 策略必须避免误删。

### 5.6 备份与恢复

当前范围不增加 Snapshot、Backup、Export、Import 或 Restore 接口，也不创建备份记录表。operator 负责备份 PostgreSQL、PrincipalHome RWX Pool 和 AgentHome PVC。产品需要内建恢复能力时再设计，不提前冻结协议。

## 6. Sandbox Provider

### 6.1 完整策略接口

Provider 是一个完整执行策略，不拆成虚假的 placement/runtime 两轴。概念接口如下：

```go
type SandboxProvider interface {
    Provision(ctx context.Context, spec SandboxSpec) (SandboxRef, error)
    Open(ctx context.Context, ref SandboxRef) (Sandbox, error)
    Inspect(ctx context.Context, ref SandboxRef) (SandboxState, error)
    Destroy(ctx context.Context, ref SandboxRef) error
}
```

配置使用 tagged union：

```text
sandbox.provider.type = local | docker | none | kubernetes
sandbox.provider.local      = {...}
sandbox.provider.docker     = {...}
sandbox.provider.none       = {...}
sandbox.provider.kubernetes = {...}
```

持久卷属于 HomeStore，不属于 SandboxProvider。切换 compute backend 不应顺带改变数据所有权。

### 6.2 `SandboxRef`

`SandboxRef` 是数据库可持久化的资源 locator，至少包含：

```text
provider
endpoint_id
resource_id
generation
spec_revision
```

Docker 的 `resource_id` 是 Container ID，`endpoint_id` 标识配置的 Docker daemon。Kubernetes 使用 namespace、Pod name 和不可变 Pod UID；新 generation 必须使用新资源身份，不能让旧 handle 命中新 Pod。

Container ID 或 Pod UID 本身不足以重建 Session。数据库还要保存 normalized `SandboxSpec` 或一个不可变 spec 引用。`Open` 必须校验资源上的 `session_id`、generation、Home ID 和 `spec_revision` label，再从 spec 恢复 policy、mount view 和 environment mapping，不能信任 daemon 中碰巧同名的资源。

Provision 不能先创建随机资源、成功后才第一次写数据库。lifecycle CAS winner 必须先持久化 `provisioning` intent，包括 generation、normalized spec、`provisioning_started_at` 和 provider 可确定性检查的 resource key；再让 Provider 以该 key 幂等创建 Pod/Container，最后 CAS 写入不可变 UID/Container ID 并进入 `ready`。create 返回 unknown 或 winner 崩溃时，Sandbox lifecycle reconciler 用 intent Inspect：label/spec 完全匹配则完成 ref，部分或冲突资源先 Destroy，资源不存在则把 intent 恢复为可重试状态。这样 external create 与数据库不能原子提交也不会产生无 registry 身份的孤儿。

迁移删除 `owner_pid` 前，Phase 3 还要按 Stella managed labels 执行一次受限 orphan audit：只处理没有匹配 SessionSandbox generation 的旧资源，未知 label fail closed 并交给管理员；不能把 daemon/namespace 中其他 workload 当垃圾。

Container ID 只在一个 Docker daemon 内有意义。初版 Docker 多副本支持要求所有 `stellad` 连接同一个 daemon，并且该 daemon 能看到同一份 Home 存储。多个独立 daemon 的资源路由不在初版范围内。

Docker volume-subpath 配置只有在 daemon capability probe 证明 API 支持时才 ready；不按客户端版本猜测，也不在不支持时退化成宿主 bind path。Docker Provider 的 distributed capability 同样要在启动时验证 shared daemon endpoint 与 reconnectable Home locator。

共享 Docker daemon 只解决 `stellad` 控制面的水平扩展。daemon 仍然是单一执行故障域；需要执行面跨节点容错时使用 Kubernetes Provider，而不是把一个 daemon 描述成集群。

### 6.3 分布式能力

| Provider 配置               | 多 `stellad` 副本 | 原因                                                          |
| --------------------------- | ----------------- | ------------------------------------------------------------- |
| local                       | 否                | 进程、文件描述符和本地 sandbox handle 不能被其他副本找回      |
| none                        | 否                | 命令直接运行在 `stellad` 所在进程环境，且只允许 unsafe gate   |
| Docker，共享 daemon         | 是                | 其他副本可通过 daemon endpoint 和 Container ID inspect/exec   |
| Docker，节点各自独立 daemon | 初版否            | Container ID 和 volume 都是 daemon 作用域，缺少 endpoint 路由 |
| Kubernetes                  | 是                | Pod 和 PVC 可通过 Kubernetes API 定位和重连                   |

这个判断属于 Provider 的当前配置，不是永久写死在 Provider 名称上的属性。

### 6.4 Secrets 兼容边界

Sandbox v2 不同时重设计 secrets。初版保持当前代码已经实现的外部语义：

- `buildSandboxEnv` 继续解析现有 Vault 和 OAuth 环境；Agent 启动的普通命令仍能看到当前允许注入的 ambient env；
- `RefreshSessionEnv` 继续在 Turn 前刷新 OAuth，并通过 `EnvRefresher` 让后续新进程使用新值；已经运行的进程不承诺原地轮换；
- group Session 不读取 human vault，现有输出 redaction 继续作为纵深防御；
- 不在本设计中新增 secret binding、per-exec 声明、审批、credential broker、egress proxy 或 Kubernetes Secret object lifecycle。

这保留了现有 ambient secret 可被 Sandbox 内命令读取的已知风险。`2026-07-04-sandbox-secrets-injection.md` 继续单独跟踪该风险和未来产品语义，但不是 Kubernetes Provider 上线的前置依赖。

Kubernetes Pod Exec 没有逐次覆盖环境变量的 API，因此 Provider 只增加最小传输 adapter：

1. 可信 `stellad` 在 AgentRun 开始和 Turn refresh 时按现有逻辑构造当前 `Policy.Env`；
2. Kubernetes adapter exec 固定的一次性 `stella-exec`，通过有长度上限的 stdin frame 发送环境，再由 helper 设置 env 并 `execve` 目标命令；secret 不进入 argv 或日志；
3. `RefreshEnv` 只更新可信 handle 中供后续 exec 使用的 snapshot，不重建 warm Pod；
4. 持久 SandboxSpec 只保存重建环境所需的非 secret 配置或引用，SandboxRef、AgentRun、PodSpec、label 和 PVC 都不保存 secret value；
5. Workspace `stella-fs` 使用固定最小环境，不触发 Vault/OAuth 解析，也不继承 AgentRun env。

这只是把现有 `Policy.Env + EnvRefresher` 契约投影到 Kubernetes transport；不是第二套 secret lifecycle，也不声称 secret 对 Agent 不可见。helper 缺失、frame 非法或 env 无法构造时 fail closed。

## 7. Session Sandbox、Agent Run 与故障恢复

### 7.1 Session 拥有 Sandbox，副本只执行 Run

同一 Agent 的两个 Session 可以由不同 `stellad` 副本同时执行；另一个副本也可以在授权后并发访问其中一个 Session 的文件：

```text
stellad A executes Agent Run R1 on Session S1
stellad B executes Agent Run R2 on Session S2
stellad C runs stella-fs on Session S1
S1 Pod and S2 Pod share the same AgentHome on one worker node
```

每个 active/warm Session 最多有一个 live SessionSandbox generation。SandboxRef、normalized spec、generation 和 lifecycle state 持久化在 PostgreSQL；支持分布式重连的任意副本都可以 `Open` 当前 generation。runner cache、Provider handle 和 event hub 是可丢弃的进程内优化，不能产生副本亲和要求。

SessionSandbox 在多个 Agent Run 之间保持 warm。超过 idle timeout 后，任意 reconciler 可以通过数据库 CAS 取得 fencing 权并删除它；对话历史和 Home 继续存在，下一次使用再创建新的 generation。这里没有需要 heartbeat 的空闲 Session owner。

AgentHome 的 RWO 放置要求同一 Agent 的 Pod 共置，不要求同一个控制面副本执行这些 Session。Agent 级 maintenance lock 只用于迁移、删除或其他必须暂停整个 Home 的操作。

### 7.2 Agent Run 排他

PostgreSQL 中持久的 AgentRun 取代当前进程内 `active sync.Map` 对正确性的承担。最小字段为：

- `run_id`、`session_id`、`run_kind`；
- `status`：`running | completed | failed | cancelled | interrupted`；
- `executor_boot_id` 和 `sandbox_generation`；
- `heartbeat_at`、`lease_until` 和 `abort_requested_at`；
- `side_effects_unknown` 和 sanitized `error_class`；
- `started_at`、nullable `execution_started_at`、nullable `finished_at` 和 `updated_at`。

`executor_boot_id` 是每次 `stellad` 进程启动生成的 UUID，不是稳定 replica identity。partial unique index 保证同一 `session_id` 最多一行 `status='running'`，另有 `(lease_until) WHERE status='running'` index 服务 expiry scan。来源领域 row 单向引用 `run_id`；AgentRun 不保存 polymorphic source ID，ingress idempotency 也属于 ingress row。

`AgentRun` 是所有顶层执行来源共用的唯一 execution lease：普通 Chat、agent-originated Session send、channel batch、Webhook、Scheduler fire、Goal attempt 和 Delegate invocation 都不能在各自领域再维护第二套 running heartbeat。各来源保留自己的业务 row，并用 `run_id` 关联 AgentRun；receipt、queue、预算、acceptance、回复发布和 retry policy 仍由来源领域负责。

数据库约束保证每个 Session 最多一个 running AgentRun。来源排队不创建 AgentRun；executor 已经具备执行容量时，才在 admission 事务中创建一行已绑定当前 `executor_boot_id` 的 running Run。在同一副本运行该 Run 的全部 AgentLoop；每个 Loop 中几十个内部 Turn 不重新 claim。普通消息通常只有一个 Loop，Goal 的 bounded repair 可以在同一 Run 和 lease 中启动额外 Loop。Run 进入终态即释放 partial unique 排他关系，下一次 Run 可以由任意副本执行并 `Open` 同一个 warm Sandbox。

PR #940 增加的 `last_turn_started_at`、`last_turn_completed_at`、`last_turn_result` 和 `last_viewed_at` 继续作为 Session 的持久 activity/unread presentation metadata。Phase 3 后，Session 的 `running` 只能由有效的 durable AgentRun 推导；watermark 的值不参加 Run admission、heartbeat、abort 或 fencing，不能被解释成第二条 execution lease。Run 派生的 started/completed/result 写入必须发生在 admission 事务或当前 AgentRun ownership CAS 下，stale executor 不能覆盖 replacement Run 的 activity；`last_viewed_at` 则保持独立的已授权 presentation write。当前 `SessionRunning`/`SessionLive` 的进程内判断必须降级为本地事件优化或删除，不能继续定义跨副本状态。

Goal 当前的 attempt lease 同时承担 River queued delivery watchdog 和 running executor lease。目标实现拆开两种责任：queued watchdog 只保留在 Goal attempt；worker 真正开始执行时才创建 AgentRun，并由它承担唯一 heartbeat。`agent_goal_attempt` 关联 nullable `run_id`，在 Run 终态后执行 Goal fold、预算和重试，不与 AgentRun 叠加执行 lease。

通用 guard 初始使用 90 秒 lease 和 20 秒 heartbeat。PostgreSQL 时间是权威；每次续租必须 CAS `run_id + executor_boot_id + status='running'`，并返回 `db_now`、`lease_until`、`abort_requested_at` 和当前 Sandbox generation。guard 以 heartbeat 请求开始时的 monotonic 时刻加上 `lease_until - db_now` 计算本地 deadline；请求在数据库取时钟前已经开始，因此该期限只会保守提前，不会晚于数据库 lease。单次 DB 失败可在最后一次已知期限内重试；CAS 失去 ownership 或 deadline 到达时，guard 必须 cancel 全部 Loop 并禁止开始新的 Sandbox 操作。每个模型、工具和 Sandbox 操作边界也必须在启动前检查 guard，不能只依赖 timer goroutine。

这里不增加固定 safety margin。提前十秒等魔法数字只能改善部分 cleanup 时延，不能阻止暂停或失联的 executor；最终互斥仍由 completion CAS、generation fencing 和旧资源删除保证。现有 Goal worker 的“heartbeat 失败只记录、executor 继续运行”行为必须随迁移删除。

generation/Pod 删除只 hard-fence Sandbox，不会停止已经离开进程的外部 API request，也不能单独保护 `stellad` 自己的数据库写入。因此所有归属于 Run 的 transcript append、memory commit、source-domain mutation 和终态写入必须在各自事务中校验 `run_id + executor_boot_id + status='running'`；stale executor 不能用 `context.WithoutCancel` 绕过该 CAS。direct input admission 把每条原始 user input 以 `input_id + run_id` 分别投影进 transcript；assistant result 必须先 durable，completion CAS 才能成功。所有 Run message 带 `run_id`，因此 abort/lease expiry 在线性化竞争中获胜时，已写 partial result 仍可明确显示为 cancelled/interrupted，而不会伪装成 completed response。interrupted input 留在历史中供查询，但不会自行触发另一个 Run。

无法由 PostgreSQL 或 Sandbox generation fence 的 outbound side effect 使用更窄语义：调用前检查 guard；平台支持时携带稳定 idempotency key；请求开始后失去 lease或网络返回 ambiguous 时记录 `side_effects_unknown + error_class`，不自动重试。迟到的外部成功无法撤销，设计只保证 stale executor 不能再提交 Stella durable state，不能声称对任意第三方系统提供 hard fencing。单纯 channel/API publish unknown 不要求销毁健康 Sandbox；只有 executor loss 或 Sandbox operation unknown 触发 generation replacement。

AgentRun admission 必须在同一事务中插入 running Run，并为目标 Sandbox generation 延长 `keepalive_until`。Run heartbeat 同时续租 lease 和 keepalive，避免 Run 开始后 Sandbox 被 idle reaper 删除。executor 在第一次模型、工具或 Sandbox 操作前先用 ownership CAS 写入 `execution_started_at`；该字段只用于保守分类和可观测性，不授予 replay。事务提交后、进程真正发起执行前崩溃时，该 Run 最终按 lease expiry 进入 interrupted；不能把它重新绑定给另一个 executor。

`execution_started_at IS NULL` 只能证明 executor 没有越过受保护的第一条操作边界，不能与外部调用原子证明更多事实；非空也可能是 marker 提交后、调用前崩溃。为这段 commit-to-start 窗口自动 requeue 仍会引入 outcome 猜测和一条 input 多 Run 的 retry protocol，因此保持保守语义：input 仍为 admitted；null marker 可显示为 interrupted-before-start，非空统一 interrupted/unknown，二者都不自动 replay。terminal Run 释放 Session 排他后，该 input 不再阻塞 lane，后续输入可以继续；“不丢”指 durable receipt 与可见终态，不承诺每条已接受消息最终得到自动回复。

每个副本运行同一个轻量 Sandbox lifecycle reconciler，与 InputDispatcher 分工但不建立 owner：

1. indexed scan 查找 `status='running' AND lease_until <= db_now` 的 AgentRun；
2. CAS winner 在一个事务中把 Run 终结为 interrupted，并把对应 SessionSandbox 切到 `fencing`、递增 generation；null execution marker 使用 `error_class=interrupted_before_start, side_effects_unknown=false`，非空 marker 保守记录 unknown；其他副本 zero-row 退出；
3. 事务外按旧 ref 幂等删除资源，确认不存在后使新 generation 可 lazy provision，并 wake 相关 source lane；Run partial unique 虽已释放，任何新 admission 仍必须要求 SessionSandbox lifecycle 可用，不能越过 `fencing`；
4. admission、busy 与 `/events` 路径遇到 expired Run 时调用同一 recovery CAS；异步 input 留在 queued 等待资源恢复，同步 Web/API 返回可识别的 `503 recovering + Retry-After`，不能把 expired row 当永久 active；
5. 同一 loop 还处理 idle Sandbox、超时 provisioning intent，以及 CAS winner 在资源删除前崩溃留下的 `fencing` row；后者重复幂等 Destroy、确认旧资源不存在，再使新 generation 可 lazy provision。删除失败记录 sanitized error/backoff 并继续重试；该 loop 不执行 AgentLoop，也不持有另一条 execution lease。

因此即使没有后续 input，expired Run 也会进入可见终态并 fence 旧资源；partial unique 不会把 Session 永久卡在 busy。

Workspace 文件操作不取得 Agent Run claim。它对当前 generation 执行 `BeginUse` 并延长 Sandbox keepalive，可以与 active Run 在不同副本并发；文件冲突仍按 POSIX 语义处理。channel FIFO 可以决定输入顺序，但不能替代数据库中的 Run 排他。

channel ordering 的稳定身份是 ChatBinding，不是会被 `/new` 替换的具体 Session ID：main chat 使用 `(principal_id, agent_id)`，group 或 private channel chat 使用现有 durable channel binding。收件事务确定 Agent 和该 binding，多个 channel 若共享同一个 main Session，也写入同一 binding lane。dispatcher 处理 `/new` barrier 后，下一条 queued input 在 admission 时通过同一 binding 解析到 successor Session；不需要搬迁队列。

v2 移除 channel `/agent` 命令以及“排队中改变目标 Agent”的语义。用户通过 Web UI、固定 channel 配置、不同 bot 或 group mention 选择 Agent。管理员修改 channel-to-Agent routing 以配置提交为线性化点，只影响之后接受的新 ingress；已进入 lane 的输入保持原 Agent/binding，但仍按 D35 重新授权。由于不存在改变 binding 的 in-band control，不增加 physical-chat routing FIFO。

group semantic auto-routing 是产品能力，不能为了实现方便退化成“必须 @mention”。它需要在 durable group message 与具体 Agent delivery 之间保留一个异步 decision boundary，但不需要两套 Agent execution state machine：

1. 每个 `ctx_group_message` 最多对应一个 `GroupRoute`，状态为 `pending | routing | routed | failed`，并保存 nullable `routing_boot_id`、`claim_until` 与 attempt；同 group 只有所有更早 seq 的 Route 已 terminal 时，当前 seq 才可 claim，不能让较快的后消息越过较慢的前消息；
2. Web 发起请求的副本可以 claim routing 并本地调用 semantic arbiter；后台 reconciler claim platform ingress，或用 `status='routing' AND claim_until <= db_now` CAS 接管失效 routing；
3. routing 只调用分类模型，不运行 Agent tool 或 Sandbox。模型 context 不得越过 PostgreSQL claim deadline，claim 丢失后取消；该调用没有产品 side effect，expired claim 可以安全回到 pending 并用新 attempt 重试，不是第二条 Agent execution lease；
4. winning completion CAS 在一个事务中持久化 responder decision，并为每个 responder 插入唯一 `(group_message_id, agent_id)` ChatBinding FIFO item；lane order 必须保持 group message seq；多 binding advisory lock 统一按 canonical key 排序取得；
5. platform/background Route completion 生成 queued items 并广播 wake。仍持有 Route claim 的 Web requester 先在本地 semaphore 预留最多 responder 数量的 executor slot，再在 completion 事务中逐 responder 重验 Session 与 lane：可执行者直接创建绑定本副本的 AgentRun 并把 input 标为 admitted；busy 或无本地 capacity 者写 `state='rejected', reject_code='busy'`，不产生可被其他副本竞争的 queued item。未用 slot 在 commit/rollback 后释放；
6. Web fan-out 部分拒绝时，其余 responder 保持本地 SSE，并为被拒 responder 发 agent-scoped error event；全部 responder 被拒时，Route 仍保存 decision/receipts，但 HTTP 整体返回 busy。若 Web claimant 在 completion 前死亡，接管者走 background queued path，客户端通过原 `client_message_id` receipt 和 transcript polling 恢复；
7. FIFO item 不带 heartbeat 或 retry lease。dispatcher admission 后由 AgentRun 成为唯一 Agent execution lease，interrupted Run 不自动 replay。

`ctx_group_outbox` 可以迁移并收窄为这个 `GroupRoute` record；它不再同时承担 fan-out execution。`ctx_group_dispatch`、其五分钟 lease/heartbeat/requeue/reaper、进程内 `sessionQueue` 和 local Publisher registry 的正确性责任全部删除。明确 mention、semantic no-mention routing、多 Agent fan-out、group event log 与 Web 本地 SSE 保持不变。

PR #962 已经为 agent-originated `session.send` 增加 `ctx_session_inbox`。Phase 3 保留它作为这个来源领域的 durable receipt、Agent provenance、精确 source/target Session 关系和 transcript-only recovery record，并增加 nullable `run_id`/终态关系；一个 receipt 最多关联一个 Run，且 partial unique `run_id` 保证一个非空 Run 也只能属于一个 inbox receipt。它不是通用输入表、ChatBinding FIFO 或 execution lease。它与 `ctx_chat_input` 的语义边界是明确的：agent send 指向调用时已经授权的精确 target Session，成功 admission 后同步等待该次 Run 的结果，不参与 ChatBinding rotation、channel ordering、batching、background dispatch 或自动 replay。

Phase 3 把 live agent send 的 receipt 与 Run admission 收敛到一个事务：重新授权 source/target，插入或确认 `ctx_session_inbox` receipt，创建并关联 target Session 的 running AgentRun，并投影幂等 inbox input；Session running partial unique index 决定唯一 winner。current-format Run-associated state 必须带 `run_id`，failed/transcript-recovered state 必须不带 `run_id`，事务 rollback 或 commit 前崩溃不能留下可被 startup recovery 误判的 current-format pending orphan。target busy、没有 executor capacity 或调用在 admission 前取消时，只提交明确 failed、unlinked receipt 并同步返回，不留下等待另一个 executor 的 durable FIFO item。事务提交结果不明时返回 outcome unknown，不能承诺没有执行。

成功 admission 后，调用保持现有同步 stream/result 语义；进程死亡或 lease 失效由关联 AgentRun 进入 interrupted，并保留 transcript，不自动执行第二次。startup recovery 只对升级前遗留或未关联 Run 的 pending receipt 执行当前的幂等 transcript append/terminalization，不创建 AgentRun、不调用模型或工具；已关联 Run 的 row 只跟随该 Run 的终态。这样 recovery 不会与另一个副本上的 live send 竞争执行。`internal/agent/session/turnqueue` 在 Phase 3 可以删除；若保留，只能在数据库 admission 前提供可丢弃的本地 fairness，不能延迟 durable receipt 后再声称排他，也不能绕过 busy/partial-unique 结果。任意副本都必须在没有该 queue 的情况下保持正确。

通用 durable input 不再抽象一张重复的 receipt 表。现有 `ctx_group_message` 已经是 group 的 immutable、deduplicated event receipt；唯一新增的通用表 `ctx_chat_input` 同时承担 direct input receipt、ChatBinding FIFO item 和一次性 Run relation：

```text
ctx_chat_input
  id
  binding_key
  binding             validated tagged union
  lane_seq
  kind                message | control
  state               queued | admitted | handled | rejected
  source
  source_scope
  source_message_id   nullable
  payload             nullable JSONB
  group_message_id    nullable FK
  agent_id
  reply_envelope
  payload_bytes
  expected_session_id nullable
  session_id          nullable
  run_id              nullable FK
  run_ordinal         nullable
  reject_code         nullable
  admission_attempts
  next_attempt_at     nullable
  last_error_class    nullable
  created_at
  updated_at
  admitted_at         nullable
  resolved_at         nullable
```

direct Web/API/DM/Webhook input 保存 normalized payload；group delivery 只引用 `ctx_group_message`，两种 payload source 必须且只能存在一种。`binding` 保存 ingress resolver 产生的稳定、非 secret coordinates，不能从 payload 或可解析字符串重新猜 authority。direct stable identity 使用 `(source, source_scope, source_message_id)` partial unique；group fan-out 使用 `(group_message_id, agent_id)` unique；batch relation 使用 `(run_id, run_ordinal)` unique。一个 input 最多关联一个 Run，因为 interrupted Run 不 replay，所以不增加第三张 join table。

收件事务在 per-binding transaction advisory lock 下从一个 PostgreSQL `BIGINT` sequence 分配 `lane_seq`；同 binding 的 lock 持有到 commit，因此顺序不会反转，rollback gap 无害，跨不同 binding 不要求伪造全局消息顺序。第一方 Web 在同一事务创建 input、running AgentRun 和 relation，或返回原 input receipt。

`/new` 是例外：receipt transaction 在授权后保存当时 binding 指向的 `expected_session_id` 或等价 monotonic revision；barrier 处理只能 compare-and-rotate 该 epoch，stale duplicate 标记 handled/no-op，不能再次归档 successor。普通 message 仍在 admission 时解析 binding 当前 Session。旧 `channel_chat_command_receipt` 回填成 `kind=control,state=handled` 的 input 后删除，避免升级后重新执行历史 `/new`。

进入 `queued` 前必须完成 payload size validation，并把附件转成 durable content-addressed media refs；不能把会在排队期间过期的平台 URL 当成附件 authority。每 binding/principal/deployment 的 backlog byte/row quota 在 acceptance 前提供 backpressure；平台 cursor/ack 只有 input commit 后才能推进，持久化失败时让来源 redeliver，不能先 ack 再丢弃。

`NOTIFY` 只广播 `{kind, opaque_binding_id}`，不选择 executor，也不携带 payload。所有副本的 control session 都收到信号；本地有空闲 executor slot 的副本通过普通 DB pool 竞争 binding transaction lock。winner 解析当前 Session、截取 compatible prefix、插入带自身 `executor_boot_id` 的 running AgentRun、更新 input relation 并延长 Sandbox keepalive；partial unique 是最终 backstop。其他副本观察到无 queued prefix 或 active Run 后退出。

每个副本运行一个轻量 `InputDispatcher`，合并 transaction `NOTIFY`、本地 capacity-release wake、启动/DB reconnect full scan 和低频 indexed safety scan。safety scan 只查询 `state='queued'` partial index，补偿通知窗口、claim winner 在 commit 前崩溃和临时 pre-admission 错误；它不是 Session owner，也不持有 execution lease。完全用 River job 代替会复制每条 input 的 durable 状态，并使 prefix batching、barrier 和 busy handoff 跨两套 queue，因此不采用。

输入 admission 保留不同入口现有的外部语义：

- Web、同步 API 和 Webhook 在已有 active Run 或 queued input 时立即返回 busy，且不得越过旧输入；
- agent-originated Session send 先以 `ctx_session_inbox` receipt 与 AgentRun 原子 admission；成功后同步等待结果，busy/no-capacity 则终结 receipt 并返回，不进入 `ctx_chat_input`；
- Telegram、QQ、Feishu 和 WeChat 等异步 channel 在平台消息已被接受后写入 durable per-ChatBinding FIFO，不能因当前 Session busy 丢消息；
- `/abort` 直接作用于 active Run；`/new` 等顺序敏感控制操作进入同一 FIFO 并成为 batch barrier。

`/abort` 可以落到任意副本。接收副本重新授权 Session 后，以 `status=running AND abort_requested_at IS NULL` 为条件 CAS 写入请求，并在同一事务中向固定 PostgreSQL channel `NOTIFY` `run_id`。每个副本的唯一 control session 负责 listener；持有该 Run 的 executor 收到通知后检查本地 active handle 并 cancel。通知不是状态存储，丢失时由 heartbeat 返回的 `abort_requested_at` 在 20 秒内兜底。

abort 与正常 completion 通过数据库顺序线性化：completion 只能在 Run 仍为 running 且没有 abort request 时提交。completion 先提交时，后续 abort 返回没有 active Run；abort 先提交时，completion CAS 必须失败。executor 确认停止后写入 `cancelled`；未响应直到 lease 到期则写入 `interrupted` 并进入 generation fencing。若取消发生在 outcome-unknown Sandbox 操作期间，同样必须 fence。接口统一返回 `202 Abort requested`，不能在 executor 确认前声称已经停止；该操作幂等且不删除 queued input。

一个 Run 完成后，已有执行容量的 dispatcher 在事务中从队首截取当前已经存在的连续兼容输入，创建一个已绑定自身 `executor_boot_id` 的 running AgentRun，并用有序关联保存每条原始 ingress。Agent Loop 一次接收整个 batch，而不是为每条消息分别启动 Run。admission 提交后到达的消息留给下一批；初版不增加 debounce。

batch 必须同时受消息数和总字节数上限约束，超限时按 oldest-first 分批，不能让 channel flood 生成无界模型输入。每条 ingress 的 idempotency key、sender、时间、附件和来源保持独立。batch Run interrupted 后保留输入与终态供查询，但不自动重放。

batch compatibility key 至少包含 authority、reply binding、`ChatBinding`、model/system/tool policy 和 run kind。sender 不进入 compatibility key：同一群组与回复出口中的多位发言者可以合批，但 assembler 必须逐条保留 speaker metadata。遇到不同 key 时在该队首截断，不能跳过中间输入，把后面的同类消息倒回前一个 batch。

queued input 只持久化稳定 source identity、normalized content/media refs、sender metadata、reply envelope 和 ChatBinding coordinates；不冻结展开后的 authority、secret、model env 或 tool policy。dispatcher 锁定一个有界队首候选后，对其中不同的 authority/ChatBinding 做 admission-time revalidation，并解析当前 model/system/tool policy，再计算 compatibility。明确的撤权或目标删除把该输入终结为 `rejected` 并保留 receipt；临时数据库或 config 错误保持 `queued`，记录 sanitized error class、attempt 与 capped backoff 后重试。policy snapshot 只存在于该 running Run 的 executor 内存中，Run 不会被接管，因此不需要 policy history 或持久 secret snapshot。

严格 FIFO 不允许把持续失败的队首偷偷 dead-letter 后跳过。payload/schema 错误必须在 acceptance 前拒绝；已排队输入若超过运维阈值仍只有 transient error，则保持 queued 并把 lane 标记为 blocked/告警。管理员可以修复配置后 retry，或以显式、审计的操作把该 input 终结为 rejected；系统不能按次数自动猜测永久失败。blocked lane 的 receipt、错误类别和 oldest age 必须可观测。

ingress dedup key 只能来自来源命名空间中的稳定 identity，例如 channel account/binding 加 platform message ID、Webhook delivery ID 或调用方提供的 idempotency key。重复 key 返回第一次写入的 ingress receipt 及其 queued/admitted/terminal Run 状态，不创建第二条 queue item，也不因原 Run interrupted 而自动重放。没有稳定 identity 时，每次 delivery 都作为独立输入保存；禁止按 body、sender、时间窗口或内容 hash 猜测去重，因为两条内容相同的合法消息不能被静默吞掉。

第一方 Session message endpoint 复用 group Web send 已有的 `client_message_id` 概念，不再增加 `Idempotency-Key` header。Web UI 必须把本地 message UUID 原样发送；兼容期 API 调用方可以省略，省略即接受每次请求都是新输入的语义。唯一范围是 `(session_id, source, client_message_id)`。duplicate 返回结构化 `409`，包含原 ingress receipt、`run_id` 和 queued/running/terminal 状态；流式 delta 没有持久化，服务端不得伪造或重放原 SSE。Web 对该错误轮询原 Run，并从 durable transcript 恢复最终消息。

running Run 永不换 executor，也不存在 claim token：正常终态或 lease expiry 释放 session partial unique 后，后续执行必须创建新 `run_id`。AgentLoop 和 Turn 不建独立表；需要持久化的用户与工具结果继续进入 transcript，Loop/Turn ordinal 只用于 Run 内 trace。executor identity 只用于正在执行的 Run，不升级为 Session owner。

### 7.3 初版 live event 降级

发起普通 Web Chat 的 HTTP 请求落到哪个副本，哪个副本就 claim 并执行 AgentRun，因此该请求返回的主 SSE 直接读取本地 Run stream，token-level 体验不受多副本影响。当前进程内 `SessionHub` 也可以继续服务恰好落到 executor 副本的 read-only attach，但它只是优化，不参与正确性。

初版不建立跨副本 AgentRun event relay，也不把 token、tool output 或 image event 写入 PostgreSQL。原因是：

1. live delta 是可丢的呈现数据，Session transcript 和 AgentRun 终态才是持久 authority；缺失 delta 不造成业务数据丢失。
2. PostgreSQL `NOTIFY` 适合 `/abort` 这类小型唤醒信号，不适合事件流。其 payload 约 8 KiB，而 tool output 和 image 可以远大于该上限；分片会把数据库变成高频广播 broker。
3. 安全的 relay 不是一个简单 handler：它需要 executor endpoint 寻址、内部认证与 TLS、稳定 event wire contract、背压、断线与 drain 语义，以及 Docker/Kubernetes 网络配置。
4. 新增 Redis、NATS 或另一套持久 event log 只为转发非权威 UI delta，会引入与需求不成比例的服务和一致性成本。
5. sticky routing 无法覆盖 Scheduler、Goal、Delegate 等 server-driven Run，也不能成为正确性依赖。

read-only `/events` 必须先查询 durable AgentRun：active 明确定义为 `status='running' AND lease_until > db_now`；expired row 先触发 D52 recovery，不能继续报告 remote active。没有 active Run 才返回 `204`；有效 Run 位于本副本时返回本地 SSE；有效 Run 位于其他副本时返回 `503 Service Unavailable`、`Retry-After` 和 `run_id`，不能谎报当前没有 Run。Web UI 对这个已知降级每 3 秒轮询 AgentRun 状态和 durable transcript；completed 时加载最终消息，cancelled/interrupted 时显示终态。当前只轮询 `/events` 而不刷新 transcript 的行为必须在多副本启用前修正。

这项延期只降低“观看另一个副本上已开始 Run”的实时性，不降低 Run admission、lease、abort、Sandbox fencing 或最终消息持久性。升级触发条件是跨副本 token-level 实时观看成为明确产品要求，或 transcript polling 的数据库成本经测量不可接受；届时优先实现仅服务 active Run 的窄化、认证 event relay，不扩展成 Session owner RPC。

### 7.4 正常复用与异常 fencing

正常完成的 Agent Run 释放 claim 后，任何副本都可以在同一有效 generation 内调用 `Open(SandboxRef)`；不需要 handoff，也不递增 generation。

executor 崩溃、Run lease 超时、网络分区或 Sandbox 操作 outcome unknown 时，后续执行按以下顺序恢复：

1. Sandbox lifecycle reconciler 或遇到 expired Run 的请求路径执行同一个 PostgreSQL CAS：将旧 Agent Run 标记为 interrupted，并把 Sandbox lifecycle 切换到 `fencing`，递增 generation。
2. 任意在途 exec/write 的结果保持 unknown，不自动重放。
3. 按旧 `SandboxRef` 停止并删除旧 Pod/Container。
4. 等待旧 Session Pod/Container identity 不存在。Kubernetes replacement 留在同一健康节点时，共享的 AgentHome RWO 可以保持 attached；只有节点故障导致 placement 整体迁移时，才等待旧节点上的相关 Pod 消失并由 CSI 完成 detach/reattach。
5. 有新操作需要时，创建带新 generation 和新资源身份的 Sandbox。
6. 挂载原 PrincipalHome 和 AgentHome，开始新的 Agent Run 或文件操作；不续跑旧 Run。

数据库 claim 只能阻止可信副本开始新工作，Docker daemon 和 Kubernetes exec API 不理解 Stella generation。失联 executor 可能仍在旧资源中发命令，因此必须删除旧资源后再创建，不能只修改数据库 owner 字段。

`ResilientSession` 只能在两次操作之间恢复资源。任何 outcome-unknown 操作都不得自动重放。Goal 可以按现有 policy 创建一个全新的 attempt、Session 和 AgentRun；这是显式的业务重试，不是续跑旧 Run。Workflow 将来可通过持久 checkpoint 和幂等 step 恢复；普通聊天 Agent Run 当前不承诺无人值守续跑。

### 7.5 Channel ingress ownership

当前 Telegram、QQ、Feishu 和 WeChat adapter 使用 long-poll 或 WebSocket。所有 `stellad` 副本同时启动同一 channel 会制造平台连接冲突、重复 delivery 和限流；数据库 dedup 只能保护后续业务状态，不能让连接风暴变正确。

初版不创建 per-channel lease、controller 或独立 Channel Gateway。所有 pull/WebSocket adapter 共用一个固定 PostgreSQL session advisory lock：

1. 每个副本复用其 `/abort` listener control session 尝试全局 lock，winner 启动全部 enabled channel ingress；
2. control session 独立于普通 transaction pool；连接健康检查失败时立即 cancel 所有 ingress；
3. graceful drain 先停止并等待 polling/WebSocket 退出，再释放 lock，不能先交棒后停旧连接；
4. 进程退出或连接被 PostgreSQL 确认断开时 lock 自动释放，其他副本再取得；failover redelivery 由稳定 ingress identity 去重；
5. provider cursor/resume token 作为 channel durable state 持久化；只有 input/group-message transaction 成功后才推进 cursor 或发送 ack，commit unknown 时依赖 stable identity redelivery；
6. webhook adapter 不取得 lock，任意副本都可以验签并持久化 ingress。

session advisory lock 要求直连 PostgreSQL，或使用 session-pooling proxy；transaction-pooling PgBouncer 不能承载它。多副本 Docker 与 Kubernetes 使用同一实现，只要求共享外部 PostgreSQL。embedded PostgreSQL 仍只支持单副本，单副本也走同一代码并自然取得 lock。

control session 通过单线程 event loop 串行处理 `LISTEN`、lock acquisition 和 health probe，避免并发使用一个 pgx connection。每个副本固定一条，不按 channel 数增长；非 leader 仍保留它接收 abort 通知，leader 只是在同一 session 上额外持有全局 lock。

channel 的 outbound Publisher 与 ingress listener 分离。任意执行 AgentRun 的副本从 durable channel config 和该 ingress 保存的 reply envelope 构建 Publisher，直接调用平台 API；不能依赖 leader 的进程内 bot/publisher registry，也不把 Run 路由回 leader。bot identity、reply metadata、ingress cursor 和 publisher 所需的可恢复状态必须持久化，进程内 client 只作 cache。

`reply_envelope` 只保存 target、thread、message ID 和 encrypted capability reference 等非 secret coordinates。Weixin `context_token` 这类回复 capability 不能继续放在 `sync.Map`，也不能明文复制到 input；ingress 使用现有 Vault encryption 按 channel/target 更新 durable capability，Publisher 在发送时重新授权并读取。QQ/Feishu 等 API client 也必须能从 durable config 独立构建，而不是要求先在本副本运行 `Start`。adapter conformance 必须证明“持久化失败不 ack/cursor advance”和“非 ingress owner 可发布”；做不到的平台 adapter 必须在多副本配置校验时 fail closed，而不是偷偷退回 leader memory。

一个 leader 管理全部连接是初版的刻意上限。只有连接数、ingress CPU 或故障 blast radius 经测量成为问题时，才把同一边界升级为 per-channel ownership；不为假想规模预先实现分片。

### 7.6 跨副本控制状态与升级

多副本启用前，以下安全状态不能继续留在进程内：

- connection OAuth device/auth-code flow 使用 PostgreSQL row 保存 `flow_id`、owner、provider、state、expiry 和 encrypted device code/PKCE verifier；poll/callback 通过 one-shot CAS 转移状态，任意副本都能完成，过期或已经 consumed 的 flow fail closed。浏览器登录 OIDC 已由签名 state cookie 自包含，不为它重复增加数据库 flow；
- 登录、注册和 authorization-server 的 IP/account rate limit 使用 PostgreSQL time 与 atomic upsert/CAS，key 只保存 keyed hash。应用副本不能各自拥有独立额度，数据库不可用时认证入口 fail closed；
- durable config mutation 在同一事务递增 monotonic revision，并发送只含 `{kind, id, revision}` 的 `NOTIFY`。control session 使本地 runner、plugin 和 Publisher cache 失效；启动、连接恢复和 cache miss 重新读取 PostgreSQL。通知只缩短 stale window，authority、Run admission 和 secret resolution 必须按当前 revision 验证，不能因漏通知继续授权。

这些事件与 abort/input wake 共用每副本那一条 serialized control session，不增加 broker 或每领域连接。

`replicas > 1` 是显式能力门槛，不是“多启动一个进程试试看”。Docker/Compose 在 Phase 4 全部验收后开放显式 multi-replica flag；Helm 在 Phase 5 的 Kubernetes conformance 通过后开放 `replicaCount > 1`。两者都校验 external PostgreSQL、direct/session pooling、distributed-capable SandboxProvider、durable channel cursor/Publisher、DB OAuth flow/rate limit 和 config revision；任一条件缺失都 fail closed，不能退回 process-local correctness。

初版要求所有 `stellad` 副本运行同一 binary/schema contract。Helm 升级先停止 admission 与 channel ingress，bounded drain active Runs，然后使用 `Recreate` 停完全部旧副本再启动新副本；超过 drain deadline 的 Run 按 interrupted/fencing 恢复。这里接受升级窗口，平台消息依靠上游 redelivery 和 durable cursor，不实现 old/new binary 同时 claim 的 rolling compatibility。以后若 zero-downtime upgrade 成为要求，再为 schema 和 durable state machine 单独设计 expand/dual-read/contract protocol。

## 8. Kubernetes Provider

### 8.1 Session Pod

每个活跃 Session 使用一个普通 OCI Pod。Kubernetes 负责节点调度、resource limit、network policy、volume attachment 和 Pod fencing。Pod 可以在 Session 空闲期保持 warm，超过配置的 idle timeout 后删除。

初版不引入 Agent anchor Pod、自定义 scheduler、VM-per-Pod controller 或独立 Executor Deployment。

### 8.2 PrincipalHome

一个部署初版只使用一个 RWX Pool PVC。Kubernetes Provider 启用时，operator 必须显式配置 RWX StorageClass 与 capacity，或提供 existing claim；chart 不猜测集群能力或安全容量。`storage_home` 给每个 typed user/group principal 分配 opaque subpath。Sandbox Pod 只挂载该 subpath：

```text
PrincipalHome RWX Pool
├── 7c1...  user U1
├── a92...  user U2
├── f04...  group G1
├── b18...  SystemSkillRoot
└── c63...  SystemAgentSkillRoot(A1)
```

目录名不使用 principal 输入。Sandbox 不能看到 Pool root。启用前必须用目标 Kubernetes/CSI 组合运行完整 POSIX conformance；失败时 Provider 不进入 ready。初版不自动扩容或分片。Pool 达到实测吞吐、容量或 CSI mount 上限后，operator 增加新 Store，新 Home 使用它；已有 Home 仍通过显式 offline migration 移动，逻辑 `PrincipalHome` 接口不变。

`stellad` Deployment 不挂载 Pool root。Kubernetes HomeStore 在 `Ensure`、`Purge`、conformance 和认证后的结构化 admin Skill 写入时创建一个短命 trusted provisioner Pod，只挂载目标 Pool root，并通过受限 `stella-fs` 操作由 registry 生成的 opaque locator。Pod 不挂 ServiceAccount token、不接收模型输入、任意 shell/代码或 AgentRun secret，操作完成即删除。provisioning CAS 保证同一 Home 只有一个 winner；helper 的 root containment 和 idempotent remove 防止路径错误跨越目标 subpath。这里不增加常驻 sidecar、controller 或第二个文件服务。

### 8.3 AgentHome 与共置

每个 `(principal_kind, principal_id, agent_id)` 首次使用时创建一个 RWO PVC。operator 必须显式配置 topology-aware RWO StorageClass 和初始 capacity，不能让每 Agent 成本由隐藏默认值倍增。StorageClass 必须使用 `WaitForFirstConsumer`、支持 expansion，并以 `Delete` reclaim policy 或 CSI 等价能力保证显式 purge 最终删除 backing volume；无法证明物理删除时 Home 保持 `purge_failed`，不能伪造 `purged`。扩容只由管理员显式请求，不自动扩容也不允许 shrink。同一 AgentPlacementKey 的 Session Pod 带稳定标签：

```text
stella.agent-home = <opaque-home-id>
```

Pod 使用 `requiredDuringSchedulingIgnoredDuringExecution` pod affinity，要求相同 label 位于同一 `kubernetes.io/hostname`。第一个 Pod 使用 Kubernetes self-affinity 的首 Pod 规则选择节点；后续 Pod 跟随。RWO PVC 是第二道约束。

Stella 不创建 `agent_placement` 表，也不保存权威 `nodeName`。Kubernetes API、scheduler 和 CSI 是实际 placement authority。节点故障时，只要旧 Pod 未驱逐或 PVC 未 detach，新 Pod 就保持 Pending。系统在这一段时间选择不可用，不冒双挂载风险。

Stella 不实现 volume attach capacity controller 或自己的存储成本 scheduler。每节点 attach limit 由 scheduler/CSI 报告并执行，deployment 总 PVC 数量和容量由 namespace `ResourceQuota` 限制。PVC 没有 Session/Pod owner reference；只有 D38 的显式 Home purge 可以删除它。

必须用 conformance test 验证目标 Kubernetes 版本和 CSI driver 的以下行为：

- 两个 Session 并发首次启动时落到同一节点；
- 单个 Session 重建不影响同 Agent 的其他 Session；
- 节点故障后先 detach 再 reattach；
- 不满足 affinity 或 volume 条件时不做无卷降级。

### 8.4 System bundle 与 Skill authority

Docker 与 Kubernetes 复用同一 digest-pinned sandbox image 和现有 `/opt/stella` layout，不创建第二套 Kubernetes tool distribution。Skill current state 只有 release bundle 与 Home filesystem 两个 authority，不存在第三个 PG/disk mirror：

- `stella-exec`、`stella-fs`、builtin Skills 和 builtin mise toolchain 作为 immutable system bundle 烘焙进 image；manifest 记录 canonical root、metadata、文件 digest/size/mode 与 bundle revision；
- image digest 与 bundle revision 进入 normalized SandboxSpec，revision 不匹配时整个 Provider fail readiness/replacement，不在 warm Pod 上 patch，也不回退到宿主抽取目录；
- local/none 按 revision 把同一 bundle 安装到 `$STELLA_HOME/bundles/<revision>`；Linux local 只读暴露当前 revision，host-execution Provider 通过 SkillView 使用精确 host path；
- `system`、`system_agent`、`user`、`user_agent` 与 `project` 直接从 D57 的 Home/root 读取。PG `skill/skill_file` 在 D59 cutover 后不再是 catalog、content 或 rebuild authority，也不再物化 Session scratch；
- system/system_agent root 对普通 Run 只读。admin API 通过 trusted `stella-fs` 写 immutable revision directory，再原子 flip root 内受限相对 symlink；catalog 同时支持这种 managed 形态与普通 POSIX Skill directory，拒绝越界 symlink；
- per-principal writable mise tree 保持在 PrincipalHome；不增加 ConfigMap 同步、Skill sidecar、宿主目录 mount、双向同步或 object checkout protocol。

隔离 Provider 把三种非 principal Skill source 映射到互不覆盖的 `/opt` 只读 execution view：

```text
/opt/stella/skills/builtin       <- exact immutable bundle revision
/opt/stella/skills/system        <- SystemSkillRoot
/opt/stella/skills/system-agent  <- SystemAgentSkillRoot(agent_id)
```

这些路径只是 Sandbox coordinate，不是 authority：builtin 来自 image/release bundle，后两者来自 shared RWX Store 的精确只读 attachment；admin writer 走独立 trusted Home access，不能经 Run mount 写入。managed catalog 在 admission 解析并 pin symlink 指向的 exact revision path，因此后续 flip 只影响新 Turn/Run，旧 revision 在引用释放前仍可读取。`none` 与无法建立 `/opt` 隔离视图的平台继续通过 `SkillView` 返回对应 exact Provider path，不伪造不可用的 mount。

Phase 0 移除旧 builtin scan 前，必须按 `listSkillRoots` 相同的 **skill-root 粒度** 比对 manifest。任何非 manifest Skill root 或无法识别的残留都进入 blocker 清单并使 bundle capability fail closed；不能只比较 top-level 目录，也不能静默隐藏运维者放在嵌套目录中的 legacy system Skill。operator 可继续旧 binary，或先通过现有 managed system 路径导入后重试。

`agent.enabled_builtin_skills` 的物理列名暂时保留，但领域层只暴露 versioned `AgentSkillPolicy`：

```json
{
  "version": 1,
  "disabled": [
    "builtin:code-review",
    "system:company-style",
    "system_agent:deploy"
  ]
}
```

legacy JSON array 无论空/非空都按当前行为解释为“无禁用”；非空值只产生 admin-visible diagnostic，首次专用 policy 写入规范化，不推断历史 allowlist。普通 Agent update 不得再写该列；policy mutation 以 Agent row lock 串行并只更新该列。dangling ref 执行时忽略但管理面可见。scope+name 不支持原地 rename；rename 等价 delete+create。

### 8.5 安全基线

初版 Kubernetes Provider 使用容器级隔离。它可在一个 tenant trust boundary 内用于生产，但部署必须满足：

- Sandbox 使用专用 worker nodes；
- 不自动挂载 ServiceAccount token；
- 非 root 用户运行并 drop all capabilities；
- 启用 seccomp，并按平台启用 AppArmor 或 SELinux；
- 禁止 hostPID、hostNetwork、hostIPC、privileged、hostPath 和 Docker socket；
- 默认拒绝 egress，只开放明确目标；
- 设置 CPU、memory、PID 和 ephemeral-storage limit；
- `stellad` 与 Sandbox 使用不同 RBAC 和网络边界。

需要 microVM 级多租户隔离的部署不应把普通 Kubernetes Provider 当成等价替代。smolvm 当前 CRI mount 实现会复制目录，不能提供本文要求的 live PVC writeback，因此不进入初始 Provider 集合。

## 9. 文件与 exec 边界

### 9.1 删除 HostPath 泄漏

公共 Session 契约不再返回宿主路径。调用方只能使用 Sandbox 规范路径，例如 `/workspace`、`/user` 和 `/tmp`。local Provider 可以在内部解析宿主路径，但不能把它泄漏给 Agent tools、Web API 或其他数据域。

文件接口覆盖当前已知消费者需要的最小操作：

```go
type Filesystem interface {
    Read(ctx context.Context, path string, opts ReadOptions) (io.ReadCloser, FileInfo, error)
    Write(ctx context.Context, path string, r io.Reader, opts WriteOptions) error
    Stat(ctx context.Context, path string) (FileInfo, error)
    List(ctx context.Context, path string) ([]DirEntry, error)
    Mkdir(ctx context.Context, path string, perm fs.FileMode) error
    Remove(ctx context.Context, path string, recursive bool) error
    Rename(ctx context.Context, oldPath, newPath string) error
}
```

精确类型和错误集合留给实施计划，但 `ResolvePath`、`ResolveWritePath` 和公共 `HostPath` 必须消失。

### 9.2 一次性 `stella-fs` helper

远程 Provider 通过原生 exec 启动一次性 helper：

- Docker 使用 Docker Exec；
- Kubernetes 使用 Pod Exec；
- local 和 none 直接调用 helper 共用的 Go `fsops` 库；
- helper 不监听端口，不运行 daemon，不持有控制面凭据；
- helper binary 由镜像提供或只读挂载；
- stdin/stdout 使用有长度上限的 framing；
- 大文件通过 `StartProcess` 流式传输，不进入 `ExecResult` 的 10 MiB capture buffer；
- helper 使用 Go `os.Root` 把操作限制在授权 mount root 内，并执行只读 mount 检查。

每次文件操作启动进程会增加一次 exec 往返。只有实测延迟成为问题时，才考虑常驻 helper；初版不为这个假设增加 sidecar 生命周期和协议协商。

### 9.3 非聊天 Workspace 文件访问

现有 Workspace API 路由都包含精确的 `agentID + sessionID`。Web UI 和 HTTP API 使用 durable Session 完成 authorization；收到请求的副本直接使用该 SessionSandbox：

1. 对 `sessionID + generation` 执行 lifecycle CAS，预留本次使用并把 `keepalive_until` 至少延长到操作 deadline；没有资源时，CAS winner 同时成为唯一 provisioner。
2. winner 创建 Sandbox；其他副本等待同一 generation 进入 `ready`，不另建资源。长操作在 keepalive 到期前续期。
3. 从持久 SandboxRef 执行 `Open`，并校验 Provider resource labels 和 generation。
4. 只启动 `stella-fs`，不授予任意 Exec。
5. helper 使用固定的最小环境，不继承 Agent Run secret。
6. 文件操作可与另一个副本上的 active Agent Run 并发，冲突遵循普通 POSIX 语义。
7. generation 在操作中被 fence 时，read 返回失败，write 返回 outcome unknown。

每个副本的 Sandbox lifecycle reconciler 都可以尝试 idle reaping，但只有一个副本能把 Sandbox row 从 `ready` CAS 到 `fencing`。CAS 仅在没有有效 Agent Run claim 且 `keepalive_until` 已过期时成功；先成功的 `BeginUse` 阻止 reaper，先成功的 fencing 使 `BeginUse` 失败并等待新 generation。这个顺序不能依赖进程内 usage counter。

非聊天文件消费者仍然不能读取 `stellad` 宿主路径。外部行为固定为：

- authorization 在可信控制面完成；
- 文件操作经过同一 `Filesystem` 契约；
- 没有运行中的 Agent Run 或 warm Sandbox 也能通过 lifecycle CAS 唤醒 Sandbox 并访问 Home；
- 控制面不会因为 local Provider 的便利重新引入 `os.*` 快路径。

任意 bash 和 CLI 继续通过 Sandbox 内的 Exec 运行，因此 Agent 看到普通 POSIX 文件系统。文件工具 API 不是 S3 object API，也不要求命令改写成对象操作。

未来出现不带 Session ID 的 Agent/Home 文件 API 时，再评估独立 file-access Sandbox。当前接口不需要第二套资源身份和 Pod lifecycle。

## 10. 一致性、缓存与 S3

### 10.1 文件一致性

两个 Agent 或 Session 同时修改同一 PrincipalHome 时，行为与同一台个人电脑上的多个进程一致。两个 Session 同时修改 AgentHome 时也一样。底层 RWX/RWO 存储必须提供经验证的 POSIX 语义，包括 rename、symlink、permission bits、file locking 和并发读写。

Stella 不承诺简化后的“last write wins”。并发覆盖、truncate、append 或 rename 的具体结果由操作和文件系统决定。Stella 只保证不额外做版本合并或透明重试。

### 10.2 Cache

- Agent cache 位于 AgentHome，跨 Session 保留，但允许按配额或空闲策略回收。
- Session cache 和 `/tmp` 随 Sandbox 删除。
- Worker `NodeCAS` 可缓存 immutable toolchain、package 或模型内容；它不是数据 authority，丢失后可重建。
- 依赖安装结果只有写入 Home 或可重建内容库后才算持久，写进 rootfs overlay 的结果不算。

### 10.3 Skill publication、Reflect 与 cutover

managed Skill create/update 不声称可以用一次 `rename` 覆盖已有非空目录。其物理形态是：

```text
<scope-root>/
├── <name> -> .stella-revisions/<name>/<tree-digest>
└── .stella-revisions/<name>/<tree-digest>/
    ├── SKILL.md
    └── ...
```

`stella-fs` 先完整写入并 fsync 新 revision，校验 tree digest，再用同目录内相对 symlink 的 rename 原子切换 `<name>`。目标 Filesystem 必须通过 symlink flip、concurrent reader 和 close-to-open conformance；无法证明就不支持 managed Skill 写。普通目录和直接 CLI 修改仍遵循 D4 的 POSIX 语义；未协调 writer 可以绕过 managed serialization，Stella 不伪造 PG 事务隔离。旧 revision 在 Phase 3 有 durable AgentRun reference 前不 GC；Phase 2 复用现有 OTel meter 暴露按 scope 聚合的 retained revision count/bytes/oldest-age，并在单 root 越过文档化容量阈值时记录带 opaque root identity 的 structured warning，避免把高基数 principal/root ID 放入 metric label。Phase 3 之后只回收没有 Run 引用且超过 grace 的 revision。

规范 `SKILL.md` metadata 保留 `status=active|deprecated`、`disable_model_invocation`、nested metadata、`created_by`、source/install timestamp 和 legacy lifecycle version。scope 来自 attachment root。active row 进入 catalog；deprecated row 在 PG cutover 时进入非 catalog migration archive，连同 changelog export 等待 operator acknowledgement。PG 不存 file mode，迁移默认 `0644`，除非现有 canonical metadata 明确证明 executable intent，禁止按扩展名猜测。

Reflect 保留 create/patch/delete 与 usage curation，但不保留 PG content authority：

- `metadata.created_by=reflect` 随文件迁移；tree digest 取代整数 version，managed writer 对同 logical ref 串行、比较 expected digest 后 symlink flip；
- 手工 POSIX edit 改变 digest，stale Reflect patch/delete 失败；移除 ownership marker 即退出 Reflect 管理；与 final compare/flip 同时发生的未协调写按普通 POSIX winner 语义处理；
- `skill_usage` 从 `skill.id` FK 迁到 `(principal_kind, principal_id, agent_id, scope, name) + last_content_digest`，只作为派生 usage telemetry；
- delete 重新校验 marker、digest、usage timestamp 与 pair activity，再把 logical entry 原子移出 active catalog；outcome unknown 不透明 retry；
- `skill_changelog` 导出归档后停止写入，不建立新的 current-state mirror。

PG→Home cutover 必须在单副本 maintenance window 完成：freeze writer，枚举每个 row/file，按 scope 写目标 root 或 archive，验证 path/owner/metadata/bytes/digest/collision/Reflect usage，再持久化 marker。marker 前 PG 是唯一 authority；marker 后 Home 是唯一 authority，server 若发现 residual PG-only current state 就启动失败。不得 dual-write、长期 dual-read 或 restore-on-miss。旧 PG 备份按 operator migration policy 保留，最后一个 reader/writer 删除后才能 drop obsolete schema。

### 10.4 S3 与 asset

PrincipalHome 和 AgentHome 的 authority 是 Sandbox 挂载的 POSIX filesystem namespace，不是某一种磁盘技术。operator 可以选择内部以 S3 保存 data block、另有 metadata/locking 层的 CSI 或分布式文件系统；只要它通过同一套 rename、symlink、permission、locking、append、concurrent write、close-to-open consistency 和 durability conformance，Stella 无需知道底层对象介质。

纯 S3 object mount 不满足该契约。Stella 不实现 object key 到文件的 checkout、generation、merge 或冲突修复，也不因为底层恰好使用 S3 而改变 Filesystem API。

`$STELLA_ASSETS_DIR` 是 PrincipalHome 中的普通 mutable 目录。user 与 group 的 bash、CLI、channel upload、Workspace API 和文件工具通过同一 Filesystem 写入后即持久；不需要 commit、watcher 或 metadata sidecar。现有 `asset.Store` 对 mutable asset 的 object authority、双写 rollback、hydrate 和 restore-on-miss 在迁移后删除，因为共享 Home 已经消除了它们原本补偿的 replica-local disk 前提。

删除该 authority 不是直接停写。若部署配置过 `STELLA_BLOB_S3_*` mutable asset authority，新版本 server 在 migration marker 缺失时 fail closed，并提示 operator 停止旧 server 后运行 `stellad storage migrate-assets`。该命令通过现有 blob listing 枚举 mutable asset keys，按 typed principal 校验 locator，把 object-only 内容安全写入 PrincipalHome，校验数量、大小与内容摘要，再以 PostgreSQL CAS 记录完成；支持 dry-run、幂等重跑和 machine-readable output，不删除远端对象。只有 marker 完成后 server 才停止 mirror/hydrate 语义。无 object authority 的部署不需要这一步。

immutable session media 仍可使用 content-addressed blob/S3。share 在发布时从 Filesystem 读取一个确定版本并保存 immutable snapshot；两者都不反过来成为 mutable asset directory 的 authority。

## 11. 迁移路线

迁移按最小可验证边界推进。每一阶段完成后都保持现有 local/Docker 用户行为可用。

| Phase | 内容                                                                                                                                                                                                                                                                                                                                                                                                                             | 验收结果                                                                                                          |
| ----- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------- |
| 0     | 生成 deterministic builtin manifest/bundle，切 catalog 到 Registry，治理 legacy filesystem system root；保留 system/system_agent wire scope；复用 Agent policy column 实现 per-Agent activation，普通 Agent update 停止覆盖 policy                                                                                                                                                                                               | builtin 不再依赖启动抽取/反扫；legacy custom root 不静默消失；policy 在 Home cutover 前后使用同一 logical ref     |
| 1     | 新增 typed `storage_home` 与 HomeStore；注册 UserHome、GroupHome、AgentHome，并创建 opaque SystemSkillRoot/SystemAgentSkillRoot identity；停止把 `agent.workspace` 当数据身份，明确 group/user-less 只读 shared Skill mount；删除时只 fence+tombstone 并保留物理字节                                                                                                                                                             | 不移动或删除现有文件，数据库能解析每个 typed Home/shared Skill root，tombstone 永久拒绝 attachment                |
| 2     | 先引入 `Filesystem`/`stella-fs`，随后用独立 Draft 实现 provider-fenced Home purge/重试/离线 Store 迁移；再加入 managed Skill symlink publication 和 trusted admin write，迁移 read/write/edit/Workspace/share/assets。通过 object-only asset marker 后，离线把 PG Skills/Reflect usage 完整迁入对应 Home root/archive，写 Skill marker，删除 ResolvePath、asset mirror 与 PG Skill current-state readers/writers                 | local、none、Docker 通过文件/Skill conformance；物理清除受 provider fence；asset/Skill 无双 authority             |
| 3     | 持久化 SandboxRef/generation/lifecycle、provisioning intent、`ctx_chat_input`、AgentRun 和 ChatBinding FIFO；把现有 `ctx_session_inbox` 接入 AgentRun admission 并限制为 receipt/transcript recovery；保留单一 GroupRoute；删除旧 process lease；实现 reconciler、event fallback、batch admission、`BeginUse`、Docker `Open`、fencing；AgentRun pin catalog/policy revision，并对旧 managed Skill revision 做 reference-aware GC | semantic routing、Skill/policy 一致性、agent send、expired Run recovery 与 execution 都不依赖进程状态或本地 queue |
| 4     | 实现 channel leader、durable Publisher、DB OAuth/rate limit、versioned config 与 Skill policy invalidation；新增共享 PostgreSQL/daemon/Home 的 3-replica Docker Compose journey，并在全部门槛通过后开放 Docker multi-replica flag                                                                                                                                                                                                | Compose 中跨副本 Run、Skill、Reflect、abort、Workspace、FIFO、leader/publisher 与 crash recovery 通过             |
| 5     | 实现 Kubernetes Provider、显式 storage 配置与 conformance gate、RWX PrincipalHome/shared Skill roots、RWO AgentHome PVC、versioned system bundle、symlink publication、Pod affinity、安全基线和 `stella-exec` adapter；复用 Phase 4 协议测试，只新增 Kubernetes platform conformance                                                                                                                                             | Session Pod 跨节点调度且不丢 Home；tool/Skill 无宿主路径或 PG scratch；通过后才开放 Helm 多副本                   |

Phase 之间不引入兼容性假象：旧代码仍需要宿主路径时，只允许 local HomeStore adapter 提供内部过渡，不能把路径重新加入新公共契约。

每个实施 Phase 在提交前执行项目规定的 `mise run format && mise run build && mise run test`。涉及进程启动、真实 HTTP、SSE、Pod/Container reconnect 或跨请求恢复时，再增加最低充分层级的 system test。

## 12. Conformance 要求

### 12.1 HomeStore

- `Ensure` 幂等，重复调用不创建第二份 Home。
- locator 不能由未验证的用户路径组成。
- tombstoned Home 不能生成新 attachment。
- Phase 1 tombstone 永久保留 identity、locator 与物理字节；它不执行 purge。
- 后续 provider-fenced purge 可重复，部分失败保留可重试状态；purged Home 只保留 immutable registry/audit metadata，identity 与 locator 不复用。
- `(principal_kind, principal_id)` 防止同 raw ID 的 user/group Home 碰撞；group Pod 不得挂载成员 UserHome。
- SystemSkillRoot 全部署唯一，SystemAgentSkillRoot 按 agent_id 唯一，locator opaque；普通 Run 只能得到适用 root 的只读 attachment。
- 现有 `users/group-{groupID}` 与其 per-Agent 内容无损注册为 GroupHome/AgentHome；GroupHome/group AgentHome 的 Skills 不冒充 user/user_agent scope；user-less Run 不把全局 Agent definition 目录当 writable Home。
- local、Docker 和 Kubernetes adapter 对同一逻辑 Home 返回一致 mount view。
- 物理实现使用 S3 的 HomeStore 仍只能暴露 POSIX locator，不能把 object key 泄漏进领域模型。

### 12.2 Filesystem

- mount root containment 和 symlink escape；
- read-only mount 写保护；
- bounded read 和流式大文件；
- mkdir、remove、rename 和 concurrent write；
- permission、executable bit、append、locking、close-to-open consistency 和 fsync durability；
- helper 被终止、Pod 被删除和连接中断时的错误分类；
- local direct library 与 remote helper 行为一致。
- object-only mutable asset 在停用 mirror/hydrate 前完整迁入 typed PrincipalHome；漏 marker 时 server fail closed，迁移命令可 dry-run/幂等重跑且不删除远端对象。
- managed Skill revision symlink 只能使用 root 内 canonical relative target；concurrent reader 在 flip 时只能看到完整 old/new tree，不能看到 ENOENT 或 mixed files；不支持该语义的 Store fail conformance。
- catalog 同时识别普通 Skill directory 与 managed symlink，跳过 `.stella-revisions`，拒绝 symlink escape；API 写和任意 POSIX 写的不同并发语义有独立测试。
- PG Skill cutover fixture 覆盖 active/deprecated/manual/Reflect/metadata-rich/binary/collision/invalid/unsupported rows；marker 前后 authority 单一，residual PG-only row 使 startup fail closed。
- legacy `$STELLA_HOME/.agents/skills` 按 manifest 的 skill-root 粒度分类，嵌套自定义 root 也必须列为 blocker，不能因 top-level 目录共用而漏检。

### 12.3 Sandbox Provider

- Provision 的幂等边界和唯一 resource identity；
- provisioning intent 必须先于 external create 持久化；create success/timeout/crash 后 reconciler 能按 deterministic resource key adopt 或 purge；
- Open 同 generation 资源；
- stale generation 拒绝或被 reconciler 销毁；
- 删除 `owner_pid` 前的 managed-label orphan audit 不触碰未知 workload；
- Destroy 幂等；
- exec outcome unknown 时不重试；
- Docker endpoint identity 和 Container label 校验；
- Docker volume-subpath 与 distributed reconnect capability 由 daemon probe 验证，不支持时 fail closed；
- Kubernetes Pod UID、generation label 和 PVC attachment 校验。

### 12.4 Session Sandbox 与 Agent Run

- 同一 Session 最多一个 live Sandbox generation；
- 任意副本可以从持久 ref `Open` 同一有效 generation；
- `status='running'` partial unique 保证同一 Session 不能并发开始两个 AgentRun；
- 来源 queue 不创建 queued AgentRun；running Run 绑定一个 `executor_boot_id`，失效后不转交；
- Chat、agent-originated Session send、channel、Webhook、Scheduler、Goal 和 Delegate 共用同一 AgentRun guard，不存在嵌套 running lease；
- Session activity/viewed watermark 继续支持 unread presentation，但 `running` 只从有效 AgentRun 推导，watermark 不参与 execution admission；
- Run 派生的 Session activity write 受 AgentRun ownership CAS 保护；stale executor 不能覆盖 replacement Run，`last_viewed_at` 保持独立授权写入；
- `ctx_session_inbox` 只保存 agent send receipt/provenance、`run_id` 关系和 transcript-only recovery；它不进入 ChatBinding FIFO、不持有 running lease，也不自动 replay；
- 每个 agent-send Run 最多关联一个 inbox receipt；current-format atomic admission 失败或崩溃不能留下 recovery-pending orphan，busy/no-capacity 只留下 failed、unlinked receipt；
- 两个副本并发向同一 target Session 执行 agent send 时，partial unique 只允许一个关联 AgentRun；loser 明确 busy/failed，不能并发调用模型、工具或 Sandbox；
- agent send 在 admission 后进程死亡时关联 Run 进入 interrupted 且不 replay；遗留未关联 receipt 的 startup recovery 只幂等补 transcript，不执行 turn；
- 删除 `turnqueue` 后全部 agent send acceptance 仍通过；若保留，它只影响单副本 fairness，不能成为正确性或 serialization boundary；
- 一个 Run 的所有 AgentLoop 和内部 Turn 留在同一 executor，Run 结束后不保留副本所有权；
- Goal queued watchdog 与 running lease 分离，Goal repair 复用同一 AgentRun；
- 第一次模型/工具/Sandbox 操作不能发生在 guarded `execution_started_at` CAS 之前；null marker expiry 仍不 replay；
- Sandbox lifecycle reconciler 在没有新请求时也会把 expired running Run CAS 为 interrupted、fence generation 并释放 Session partial unique；
- reconciler winner 在 fencing CAS 后崩溃时，另一副本能从持久 `fencing` state 继续 Destroy 并解除 recovering；
- admission/busy/`/events` 遇到 expired Run 时触发同一 recovery，不能永久返回 busy/remote active；
- heartbeat CAS 失去 ownership 或无法在 deadline 前续租时，executor fail closed；
- stale executor 的 transcript/memory/source-domain write 被 Run CAS 拒绝；第三方 side effect unknown 不自动重放；
- 本地 deadline 从 PostgreSQL 剩余期限保守换算，不依赖固定 margin 或本地 wall clock；每次操作前都重新检查；
- `/abort` 持久化后即使 `NOTIFY` 丢失也会被 heartbeat 观察；completion 不能覆盖已请求的 abort；
- `/abort` 不删除 queued input，返回 accepted 而不是伪造同步完成；
- 发起 Run 的主 SSE 保持本地直连，不经过 PostgreSQL 或 replica relay；
- 远端 read-only attach 返回可识别的 `503`，Web 轮询 durable Run 状态和 transcript；只有确实没有 active Run 才返回 `204`；
- busy Web/API/Webhook fail fast，不能越过更早的 queued channel input；
- queued channel input 按队首连续兼容 batch 原子进入一个 Run，claim 后的新输入留到下一批；
- channel admission 使用当前 authority/policy；撤权输入可查询为 rejected，临时解析错误不消费队首；
- ChatBinding FIFO 跨 `/new` rotation 保序；管理员改路由只影响新 ingress，channel `/agent` 不再可用；
- 所有 channel 不再注册或宣传 `/model`，Agent model 只能通过授权 Web/API 配置修改；
- 两个针对同一旧 Session 的 `/new` 只有第一个 compare-and-rotate 成功，第二个不能归档 successor；
- GroupRoute 按 group seq materialize，后消息不能先进入任一 Agent lane；其 delivery 不维护 AgentRun 之外的执行 lease；
- expired GroupRoute claim 可以 CAS 接管；stale claimant 的 completion CAS 失败且不能重复 fan-out；
- active Web claimant 遇到部分 responder busy 时只把对应 input 终结为 `state=rejected, reject_code=busy`，其他 responder 保持本地 SSE；全部 busy 不产生 queued fallback；
- batch 上限按 oldest-first 切分，barrier 两侧不能合并；
- 不同 reply binding、authority 或 execution policy 不能合批，也不能通过跳过队首重排；
- stable ingress identity 的 redelivery 返回原 receipt，不重复排队；无 identity 的相同内容保留为两条输入；
- 第一方 Web 重试复用 `client_message_id`；duplicate `409` 驱动 Run polling 和 transcript reload，不触发第二个 Run；
- group input 引用 `ctx_group_message` 而不复制 payload；direct input 自带 payload，constraint 拒绝零个或两个 source；
- `NOTIFY` 广播不指定 executor；有容量副本竞争 binding admission，partial unique 阻止双 Run；
- startup/reconnect scan 与 indexed safety scan 能处理丢失 wake、claim 回滚和临时 pre-admission failure；
- transient admission poison-head 使用 capped backoff、blocked-lane alert 与 audited admin reject，不自动跳过；
- commit-to-start crash 产生可查询的 interrupted-before-start，既不 replay 也不阻塞后续 lane；
- Workspace 操作可以在其他副本并发 `BeginUse + Open`；
- AgentRun admission pin 当前 Skill catalog observation 与 AgentSkillPolicy revision；运行中 policy change 只影响下一 Run，disabled winner 不回退低层同名 Skill；
- managed Skill revision 只有在没有 AgentRun 引用且超过 grace 后才 GC；
- `BeginUse` 与 idle reaper 之间不存在 use-after-fence 窗口；
- expired Run 或 outcome-unknown 操作递增 generation，先删除旧资源再创建新资源。

### 12.5 Kubernetes

- Provider 缺少显式 RWX/RWO class/claim 或 capacity 时配置校验失败；
- PrincipalHome Pool 必须先通过目标 CSI 的 POSIX conformance，失败时不进入 ready；
- trusted provisioner Pod 的 opaque subpath containment、幂等 Ensure/Purge、结构化 admin Skill write 和 crash cleanup；模型输入不能成为其命令/路径 authority；
- 同 Agent Pod 并发首启共置；
- 不同 Agent 可分布到不同节点；
- UserHome 与 GroupHome 跨节点可见且 namespace 隔离；
- AgentHome 不发生跨节点双挂；
- Kubernetes 新 exec 观察当前 `Policy.Env` snapshot；refresh 不重建 Pod，secret value 不进入 PodSpec、SandboxRef 或日志；
- `stella-fs` 不解析或继承 AgentRun secret；
- node failure、Pod deletion、volume detach 和 replacement 顺序；
- 同节点 Session replacement 不要求共享 AgentHome detach，跨节点迁移必须等待 CSI detach；
- security context、RBAC、network policy 和 resource limit fail closed。
- Sandbox image digest/bundle revision 匹配 normalized spec；custom image 缺少相同 bundle 时 fail readiness 并给出 rebuild contract；
- SystemSkillRoot/SystemAgentSkillRoot 跨节点只读可见，managed symlink flip 通过目标 CSI conformance；Session 不物化 DB Skill scratch；
- PVC attach limit 由 scheduler/CSI 执行，ResourceQuota 拒绝超额创建时 fail closed。
- AgentHome StorageClass 缺少 `WaitForFirstConsumer`、expansion 或可证明的 purge 能力时配置校验失败。

### 12.6 Channel

- 多副本只存在一个 pull/WebSocket ingress leader；Webhook 不受该 lock 限制；
- owner DB session 失效即停止 ingress，graceful handoff 先停 listener 再释放 lock；
- 每个副本只有一条 pool 外 control session，复用 abort `LISTEN`、health probe 和全局 lock；
- Docker 与 Kubernetes 共用外部 PostgreSQL 语义，transaction-pooling proxy 被配置校验拒绝；
- failover redelivery 由 durable ingress identity 去重；
- ingress commit 失败或 unknown 时不推进 provider cursor/ack；leader failover 从 durable cursor 恢复；
- 任意副本可以从 durable config/reply envelope 构建 Publisher，不依赖 owner 进程内 registry；
- Weixin 等 reply capability 只经 encrypted Vault reference 解析，不进入 input 明文或进程唯一 cache；
- 单 leader 成为实测瓶颈前不实现 per-channel lease。

### 12.7 跨副本控制状态与升级

- connection OAuth flow 在创建副本退出后仍可由另一副本安全完成；并发 callback/poll 只有一个 one-shot CAS winner；
- PKCE verifier、device code 和 reply capability 只以 encrypted secret 保存，日志、`NOTIFY` 和 API receipt 不泄漏；
- 登录/注册 rate limit 在副本间共享 PostgreSQL authority，轮换负载均衡目标不能绕过额度；
- config `NOTIFY` 丢失或 control session reconnect 后，revision validation/full refresh 仍阻止过期授权或 policy admission；
- AgentSkillPolicy mutation 在 PostgreSQL 同行事务更新 canonical policy digest 并通知；两副本并发 managed Reflect write 对同 logical ref 只有一个 expected-digest winner，漏通知时下一次 admission 仍按 durable digest 刷新；
- Phase 4 前 Docker/Compose 拒绝 multi-replica，Phase 5 前 Helm 拒绝 `replicaCount > 1`；门槛开启后缺失任一 shared correctness capability 仍启动失败；
- upgrade drain 后不存在 old/new binary 并发 claim；超时 Run 进入 interrupted 并按 generation fencing 恢复。

## 13. 当前不做

- 持久个人 VM 或 guest rootfs；
- 保留运行进程、shell、socket 或 machine identity；
- outcome-unknown 命令的透明 replay；
- Stella 直接实现的 S3 object filesystem、checkout/merge、MVCC workspace 或自动冲突合并；
- 内建 backup、snapshot 和 restore；
- 在线 Home 双写迁移；
- PG 与 Home 之间的 Skill 双写、长期 dual-read、watcher/sync engine 或 restore-on-miss；
- cutover 后把 PG catalog/index 当 Skill current-state authority；只有按 logical ref 保存的派生 usage telemetry 可继续存在；
- 独立 Executor Fleet 或 Stella 自己的 compute scheduler；
- smolvm、Kata、OpenSandbox 和通用 Remote Provider；
- 普通聊天 Agent Run 的 checkpoint/resume；
- 多 Docker daemon 的资源路由；
- 为文件访问部署常驻 sidecar；
- Project Skill HTTP host-path gap 的实现；由 CherryHQ/stella#928 在既定 Filesystem/Home 边界内修复，不改变本设计方向；
- 跨副本 token-level SSE/WebSocket event relay 或 broker；初版使用 durable polling fallback；
- Vault/OAuth 的产品语义重设计、credential broker 或 egress proxy；由独立 secrets design 推进；
- per-channel ingress lease、独立 Channel Gateway 或 owner-directed AgentRun routing；由实测规模触发；
- channel 内联 `/agent` 切换与为其服务的 physical-chat routing FIFO；
- channel `/model`、只读替代命令或 per-ChatBinding model override。
- mixed-version rolling upgrade 和 zero-downtime schema contract。
