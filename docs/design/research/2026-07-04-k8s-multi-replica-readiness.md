# Stella Kubernetes 无状态多副本就绪度审计

- **日期**: 2026-07-04
- **方法**: 6 个 codex (openai-codex/gpt-5.5) read-only 并行扫描六个维度(本地文件系统 / 内存态单例 / 后台任务重复执行 / IM 通道 ingress / 启动与生命周期 / 沙箱与进程亲和性),由 Claude 交叉 review,高影响结论逐条抽查源码核实。
- **范围**: 仅 `stellad` server 可达代码;CLI-only 路径只做备注。不含任何代码修改。

## 总判定

**当前 stellad 不是 stateless multi-replica ready。**

- **单副本上 K8s(replicas=1 + `Recreate` 策略)**:可行,但有前置条件(外部 PostgreSQL、PVC 挂 `STELLA_HOME`、健康探针、shutdown 宽限、sandbox 策略)。
- **多副本**:会立刻炸的地方有三类——IM 通道每副本全量启动、per-session 串行化/SSE 推送是进程内存态、`STELLA_HOME` 本地文件被当持久数据用。

值得肯定的是:**任务执行面已经基本 River 化**(scheduler / goal / embedding / reflect 都走 Postgres 队列,attempt 有 lease + reaper),migration 有 `pg_advisory_lock`,群聊 eventlog 用 DB advisory lock + 幂等去重,group outbox/dispatch 有 DB lease claim。多副本改造的地基是有的,烂的是边缘:通道 ingress、Web 推送、文件、内存单例。

## 已确认安全(无需改动)

| 领域                              | 证据                                                     | 机制                                                                                   |
| --------------------------------- | -------------------------------------------------------- | -------------------------------------------------------------------------------------- |
| DB migration 竞态                 | `internal/db/database.go:303`                            | goose + River migration 全程持 `pg_advisory_lock`,N 副本串行等待                       |
| Scheduler 任务执行                | `internal/scheduler/river.go`                            | River 队列 + unique opts + `LockSchedJobForRun` 运行级锁;periodic 由 River leader 负责 |
| Goal attempt 执行                 | `internal/goal/dispatcher.go:473`、`begin_attempt.go:27` | claim + enqueue 同事务,advisory goal lock,lease + heartbeat + reaper,跨副本安全        |
| Embedding backfill                | `internal/embedding/river.go:265`                        | River periodic + ByState unique,集群内单实例                                           |
| Reflect review                    | `cmd/stellad/setup_reflect.go:31`                        | 作为 scheduler builtin job 走 River,不会每副本各跑                                     |
| 群聊消息去重/排序                 | `internal/eventlog/eventlog.go:101`                      | 事务 + per-group advisory lock + platform_message_id 幂等                              |
| Group outbox/dispatch             | `pkg/db/sqlc/ctx_group_outbox.sql.go:54`                 | DB status/lease claim,每副本轮询但仅一方认领                                           |
| OIDC 登录 state                   | `internal/auth/oidc/state.go`                            | 签名 cookie,无服务端状态(要求各副本 `STELLA_VAULT_KEY` 一致)                           |
| Vault / credentials / pluginstate | `internal/vault/service.go` 等                           | 存 PostgreSQL                                                                          |
| DB 中无副本本地句柄               | SQL 全查无 PID/socket/container_id 持久化字段            | 不存在"B 副本拿 A 的句柄"毒药                                                          |

## Blockers(多副本立刻出错)

### B1. IM 通道 ingress 每副本全量启动

`cmd/stellad/gateway.go:280` 启动时 `applyManagedChannelPlugins` 遍历所有 channel 逐个 `ApplyChannel`(`cmd/stellad/channel_runtime.go:26-48`),`pkg/plugins/bot_runtime.go:132` 直接 `go ch.Start()`。**没有任何 leader election / 分片。** 每个通道的具体后果:

| 通道     | 入站机制                                                                                                                                   | 2 副本后果                                                                   |
| -------- | ------------------------------------------------------------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------- |
| Telegram | LongPoller(`plugins/channels/telegram/telegram.go:53`)                                                                                     | 同 token 双 `getUpdates` → Telegram 409 Conflict 互踢,或消息随机分给某一副本 |
| QQ       | WebSocket(`plugins/channels/qq/qq.go:101`)+ 每进程 token refresh                                                                           | 双 WS session;重复投递双回复,或平台拒绝并发连接抖动                          |
| Feishu   | WebSocket(`plugins/channels/feishu/feishu.go:159`)+ **内存去重** `seenMsgs`(`feishu.go:70`)                                                | 双 WS;跨副本去重失效,DM 双回复(群聊靠 eventlog 能压住)                       |
| 微信     | iLink long-poll + **内存 cursor**(`plugins/channels/weixin/weixin.go:31-34`)+ `contextTokens sync.Map` + 启停自报 `NotifyStart/NotifyStop` | 双副本各自空 cursor 重复消费;某副本下线的 `NotifyStop` 误伤仍在线的副本      |
| Web      | HTTP + SSE                                                                                                                                 | 入站无重复问题;SSE 见 B3                                                     |

**修复方向**:每个 channel instance 一个 Postgres advisory lease(单 active pod),或 Telegram/Feishu 改 webhook 模式(天然无状态入口)+ DB 去重。微信的 cursor/contextToken/启停通知必须租约化。

### B2. Per-session 串行化是进程内存态 → 跨副本并发 turn

- `internal/agent/runtime/runtime.go:28`:`active sync.Map` 是"每 session 只有一个 in-flight turn"的唯一守卫,进程本地。
- `internal/channel/session_queue.go:19`:channel 侧 per-session FIFO 也是 `map[string]*sessionSlot` + goroutine。

同一 session 的两个请求落到不同副本 → 两个 agent turn 并发执行:历史重复写、工具/沙箱副作用并发、`/abort` 打到 B 取消不了 A 上正在跑的 turn。**修复方向**:DB 级 per-session turn lease(advisory lock 或 lease 行),sticky session 只能当短期缓解。

### B3. 实时事件推送(SSE)是进程内 hub

`internal/agent/runtime/hub.go` 的 `SessionHub` 只存在于执行 turn 的那个进程;`internal/server/sessions.go:417` 的 `StreamSessionEvents` 依赖本进程 `SubscribeSession`/`SessionLive`。turn 在副本 A 跑、浏览器 SSE 重连到副本 B → B 认为 session 不 live,直接 204,用户看不到实时输出。全仓库**没有任何 LISTEN/NOTIFY 或外部 pub/sub**。**修复方向**:Postgres LISTEN/NOTIFY(已有 PG,不用引 Redis)广播 turn 事件,副本本地 hub 做 fanout;重连兜底读 DB transcript。

### B4. `STELLA_HOME` 本地文件被当持久数据

写入路径(均为 server 运行时可达):

| 数据                 | 路径                                                     | 证据                                               | 性质                                                           |
| -------------------- | -------------------------------------------------------- | -------------------------------------------------- | -------------------------------------------------------------- |
| 用户/agent 工作区    | `$STELLA_HOME/users/{u}/agents/{a}/...`                  | `internal/agent/workspace.go:83-116`               | **持久用户数据**                                               |
| 上传与通道附件       | `$STELLA_HOME/users/{u}/data/assets`                     | `internal/server/sessions.go:1394`、各通道 handler | **持久用户数据**                                               |
| Recally 文章正文     | `$STELLA_HOME/library/{u}/articles/...`(DB 只存相对路径) | `internal/recally/files.go:24-31`                  | **持久用户数据,DB 行在、文件只在一个 pod 上**                  |
| Skills disk mirror   | `$STELLA_HOME/.agents/db-skills` 等四处                  | `internal/skills/disk_sync.go:147-186`             | DB 是 source of truth,但运行时读磁盘镜像,跨副本会读到旧/缺文件 |
| models.json 缓存     | `$STELLA_HOME/cache/models.json`                         | `internal/config/models_cache.go:25`               | A 上 fetch 的模型列表 B 看不到                                 |
| mise 工具链/插件状态 | `$STELLA_HOME/.mise-tools`、`plugin-manifest-state.json` | `internal/manifestplugins/reconcile.go:55`         | 每副本各装一套;共享目录则并发写冲突                            |

**修复方向**:分层处置——用户数据(工作区/上传/文章)→ 对象存储或共享 RWX PVC;skills/models 缓存 → 直接读 DB 或加跨副本失效;工具链 → 镜像内置或 per-pod 幂等安装。注意:**多副本共享一个可变 `STELLA_HOME` 与每 pod 独立卷都各有坑**,必须按目录拆策略,不能一刀切。

### B5. 嵌入 PostgreSQL 模式

`cmd/stellad/commands.go:90-95`:`STELLA_DATABASE_URL` 未设置时每个 pod 在 `$STELLA_HOME/postgres` 启动自己的嵌入 PG → 每副本一个独立数据库,状态分裂。**K8s 部署必须强制外部 PostgreSQL,嵌入模式应显式拒绝多副本。**

### B6. 启动 Seed 竞态

`internal/store/dbstore.go:660+`:`Seed()` 是 read-then-insert(`ListAgents` 为空才 `CreateAgent`),`agent` 表只有 id 主键,无默认 agent 的自然唯一键;默认 channel seed 同理。空库上两个副本同时启动 → 两个 "Stella" agent。**修复方向**:自然唯一键 + `ON CONFLICT DO NOTHING`,或复用 migration 的 advisory lock 包住 seed。

## Major(不炸但错,上多副本前应修)

| #   | 问题                                 | 证据                                                                                                                                                       | 后果 / 方向                                                                                                                                                                                                                                        |
| --- | ------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| M1  | 配置/凭据失效只广播到本进程          | `internal/agent/pool_manager.go:620-676`、`runner_cache.go:31`、`internal/server/vault.go:220`                                                             | 在 A 改 vault/OAuth/agent/provider/plugin,B 的 runner 继续用旧密钥旧配置。方向:PG NOTIFY 失效总线,各副本收到后本地 reset                                                                                                                           |
| M2  | OAuth flow 状态在内存                | `internal/credentials/oauth/store.go:5-9`(注释自认)                                                                                                        | auth-code callback / device-flow poll 打到另一副本 → "unknown or expired flow"。callback 不受 LB stickiness 保证,必须 DB 化(带 TTL)                                                                                                                |
| M3  | 登录限流在内存                       | `internal/auth/ratelimit.go:40` `sync.Map`                                                                                                                 | N 副本 = 限额放大 N 倍,爆破冷却可绕过。DB/Redis 化                                                                                                                                                                                                 |
| M4  | 无 K8s 健康探针                      | 仅 `/api/status`(`internal/server/status.go:13`),匿名响应不查 DB                                                                                           | 加匿名 `/healthz`(存活)+ `/readyz`(DB ping + migration 完成),ready 失败返回 503                                                                                                                                                                    |
| M5  | Shutdown 宽限不现实                  | HTTP shutdown **2s**(`cmd/stellad/gateway.go:296`),River SoftStop 固定 30s(`internal/db/river.go:63`)                                                      | 滚动更新会砍断 LLM turn。goal attempt 有 lease reaper 兜底(业务状态可恢复,本地副作用丢失);前台 chat turn 无恢复。两个超时都要可配,配合 `terminationGracePeriodSeconds`                                                                             |
| M6  | Sandbox 部署假设搬不进 K8s           | Dockerfile 装 bubblewrap;compose 挂 `/var/run/docker.sock`(DooD);docker orphan cleanup 用 `owner_pid=os.Getpid()`(`plugins/sandbox/docker/session.go:179`) | K8s 默认无 docker sock,bwrap 受 seccomp/userns 限制;容器内 PID 判存活在 DooD 下完全失效(误清/漏清)。方向:明确 K8s sandbox 支持矩阵;orphan 归属改 pod UID/实例 UUID + 心跳                                                                          |
| M7  | manifest plugin reconcile 每副本执行 | `cmd/stellad/setup_plugins.go:129`                                                                                                                         | 并发写同一 state 文件/重复装工具。方向:分布式锁或 init Job                                                                                                                                                                                         |
| M8  | 地址与 URL 默认值                    | `--host` 默认 `127.0.0.1`(Dockerfile 才覆盖);`STELLA_SERVER_URL` 默认 loopback;`STELLA_BASE_URL` 缺省用 bind 地址                                          | OAuth redirect / Feishu deep link 会生成 localhost URL。Helm 必填 `STELLA_BASE_URL`/`STELLA_DATABASE_URL`/`STELLA_VAULT_KEY`                                                                                                                       |
| M9  | DM/C2C 无 DB 去重                    | `internal/channel/coordinator.go:293`:只有群聊走 eventlog                                                                                                  | 通道层一旦多副本(或平台重投),私聊消息双执行。加 `(platform, chat, platform_message_id)` 去重表                                                                                                                                                     |
| M10 | 执行面文件亲和性                     | runner/工作区/容器 sandbox 全是副本本地(`internal/agent/runtime/runner_cache.go:28`、`plugins/sandbox/docker/session.go:134`)                              | pod 死后 goal 会重试新 attempt(DB 正确),但上一次沙箱里的本地成果不可恢复;跨副本无法"接管 live sandbox"。**same-sandbox acceptance 设计本身是对的**——acceptance 在 runner close 前同步跑在同一 River worker turn 内,不会被另一副本接手,只是不可迁移 |

## Minor / 备注

- `internal/auth/linkcode.go`:生产走 DB 共享 store(好),但 `ensureSchema` 会 `DROP TABLE`——滚动重启会清掉 5 分钟内的 link code,UX 小坑。
- `internal/groupingest`:目前**没有**在 server boot 接线;如果将来以 ticker 接入,其 `tryLock` 是进程本地,多副本会重复抽取(重复 LLM 花费)。
- `$STELLA_HOME/pg-runtime`、`$STELLA_HOME/bin`、builtin skills 抽取:副本安全的启动产物,但 emptyDir 每次重启重抽;建议烤进镜像。
- `internal/scheduler/service.go:252` `ScheduleEvery` 是裸 ticker,当前生产 boot 未使用;留意别新增调用。
- Debug dumps 写 `$STELLA_HOME/dumps`,K8s 下应改 stdout。

## 建议路线图

**Phase 0 —— 单副本先上 K8s(改动最小)**
外部 PostgreSQL 强制化;`STELLA_HOME` 挂 RWO PVC;加 `/healthz` `/readyz`;shutdown/soft-stop 超时可配 + 足够的 termination grace;Helm 必填 `STELLA_BASE_URL`/`STELLA_VAULT_KEY`;sandbox 策略显式声明(bwrap 权限或 none)。`replicas=1` + `strategy: Recreate` 即可稳定运行,以上多数是配置/小改动。

**Phase 1 —— API 层多副本**
SSE 事件 fanout 走 PG LISTEN/NOTIFY;per-session turn lease DB 化(同时覆盖 channel FIFO 语义);配置/凭据失效总线;OAuth flow store 与限流 DB 化;Seed 加唯一约束;DM 去重表。做完这层,**Web/API 流量可以水平扩**。

**Phase 2 —— 通道与执行面**
channel instance 级 advisory lease(单 active 消费者)或 webhook 化;用户文件外部化(对象存储);docker orphan 归属改 pod UID + lease;manifest reconcile 上锁。

**架构上要接受的事实**:这套系统有天然 stateful 的执行面(沙箱、工作区)。目标不是"假装 stateless",而是**API/推送层无状态可扩 + 执行面显式亲和(River worker 就是天然的执行分片)+ 文件持久层外部化**。

## 附录:原始扫描报告

6 份 codex 原始报告(会话 scratchpad,阅后即焚,关键内容已并入本文):
`codex-fs.md` / `codex-mem.md` / `codex-dup.md` / `codex-channel.md` / `codex-lifecycle.md` / `codex-sandbox.md`

抽查核实过的关键证据:migration advisory lock、`runtime.active` sync.Map、OAuth FlowStore、RateLimiter、Telegram LongPoller、Feishu `seenMsgs`、微信内存 cursor、recally 文件路径、Seed read-then-insert、2s HTTP shutdown、`/api/status` 匿名行为——全部与报告一致。
