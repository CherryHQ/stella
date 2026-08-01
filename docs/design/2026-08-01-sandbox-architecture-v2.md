# Sandbox 架构 v2:控制面/执行面分离与契约完备化

- **日期**: 2026-08-01(rev 2)
- **状态**: Draft,待评审
- **范围**: sandbox 域模型(Workspace/Machine/Session)、`pkg/sandbox` 契约、`internal/agent/sandbox` 核心工具与 skill/workspace 视图、控制面的 sessionless 文件消费者、machine 生命周期、执行面进程分离的立项路线。不含 agent loop 可恢复性(见 §10)。
- **关联**: `docs/design/2026-07-04-sandbox-secrets-injection.md`(secret 注入语义以该文档为准,本文只定义它在新架构中的位置)

## 1. 问题:巧合耦合

`Session` 接口(`pkg/sandbox/session.go:12`)名义上是后端无关的沙箱契约,实际依赖一个未声明的前提:**沙箱与 stellad 共享宿主文件系统**。这个前提对现有三个后端(local/bwrap、docker bind mount、none)碰巧成立,但它是巧合,不是设计。泄漏点:

1. **路径解析返回宿主路径,文件 I/O 在契约外**。接口注释明示 "use os.* with the resolved path"(`pkg/sandbox/session.go:23-28`);`read`/`write`/`edit` 三个核心工具解析出宿主路径后直接 `os.ReadFile`/`os.WriteFile`(`internal/agent/sandbox/read.go:95`、`write.go:55`、`edit.go:56,70`)。
2. **控制面存在一批完全绕过 Session 的文件消费者**。web/API 层的 workspace 浏览与读取直接在宿主 FS 上 `OpenSafeRoot` + `ReadFile`(`internal/agent/session/access/workspace.go:227-247`),内嵌 assets 懒恢复(`assets.Restore`,同文件 :235);scoped skills 内容宿主侧读取(`internal/server/skills_scoped.go:415`)。这些消费者在**没有任何活跃 session** 时也要工作——而 session 是 10 分钟 idle 回收的(`internal/agent/pool_manager.go:179`)。
3. **xberg 在宿主上解析沙箱文件**。`read` 对二进制文档 shell out 到宿主的 xberg 二进制(`internal/agent/sandbox/read.go:239-253`)。文档解析器吃 agent 写入的不可信文件,是独立于路径耦合的攻击面。
4. **Mount 语义即宿主 bind mount**。`Mount{HostPath, SandboxPath}` 和 `FilesystemPolicy`(`pkg/sandbox/policy.go:74-93`)把"宿主目录是数据真身"写进了策略类型。
5. **后端知识散落在调用方**。`isolatingBackend()` 对后端名做字符串比较来决定路径视图(`internal/agent/sandbox/skill_view.go:83-86`);新增后端必须记得来调用方登记。
6. **执行状态是进程内的、一次性的**。session 缓存在内存(`internal/agent/runtime/runner_cache.go`),DB 无任何沙箱记录;docker 后端启动时主动 reap 孤儿容器(`plugins/sandbox/docker/session.go:142-156`)——系统假设"遗留执行状态即垃圾"。

后果:任何"磁盘不在宿主上"的后端(microVM、远程 worker 节点、Kubernetes runtime)都无法在不破坏上述假设的情况下接入;执行面无法独立于 stellad 升级、重启、扩容;宿主承担了本应隔离在沙箱内的解析工作。

## 2. 目标形态

```text
stellad(控制面)
  用户 / Agent / 对话 / Goal / Workflow / Vault / 审计 / 策略决策
        │
        │  窄契约:workspace-fs · session lifecycle · exec · stream
        │          capabilities · secret-by-reference · audit events
        ▼
Sandbox Backend / Worker(执行面)
  ├─ workspace store(数据面,独立于 machine 状态可访问)
  ├─ machine registry + reconcile(DB 持久,重启后重连)
  ├─ credential broker / egress proxy(真 secret 只活在这一层)
  └─ machines(bwrap | microVM | container —— worker 内部实现细节)
```

**核心原则:stellad 把每个沙箱都当远程对待,本地是远程的特例。** 现在是反的——远程被当成本地的特例,所以宿主路径假设泄漏到工具层、视图层、策略类型和 web/API 层。

## 3. 域模型:Workspace / Machine / Session 三分

旧稿(rev 1)只有 Session 一个名词,把文件 API 挂在 Session 上——这对 §1.2 的 sessionless 消费者是根本性错配:为浏览一个文件而拉起沙箱是荒谬的。正确的域模型是三个名词、两条面:

| 名词          | 是什么                                                         | 生命周期                                   | 谁消费                                            |
| ------------- | -------------------------------------------------------------- | ------------------------------------------ | ------------------------------------------------- |
| **Workspace** | 数据:agent 的持久文件集(workspace root、user data)             | 与 agent 同寿命,独立于任何 machine/session | 核心工具、web UI 浏览、goal artifact、assets 恢复 |
| **Machine**   | 计算:一台可执行命令的隔离环境(进程、内核、网络)                | 后端决定:临时(每 session)或持久(§7 租约)   | Session 独占或复用                                |
| **Session**   | 租约:一次 agent 会话对一台 machine 的使用权,workspace 挂载其上 | 短命,idle 回收(现状不变)                   | runner、四个核心工具的 exec 面                    |

关键设计决策:**workspace 归执行面的存储服务所有,但不归 machine 的系统盘所有。** machine 挂载 workspace;workspace store 在 machine 停止甚至不存在时也能提供文件访问。docker 后端的 volume 模式(`STELLA_DOCKER_SANDBOX_MODE=volume`)已经是这个形状的先例;本地后端的 workspace store 就是宿主目录(恒等映射,现状不变)。这个决策同时回答了三个旧稿悬而未决的问题:sessionless 访问、workspace 备份/导出、machine 重建时数据不丢。

## 4. 契约 v2

### 4.1 文件面:`FS` 接口,两个提供者

```go
// FS 是沙箱文件访问的统一契约。path 使用提供者声明的规范路径。
type FS interface {
        ReadFile(ctx context.Context, path string) (io.ReadCloser, error)
        WriteFile(ctx context.Context, path string, r io.Reader, perm fs.FileMode) error
        Stat(ctx context.Context, path string) (FileInfo, error)
        ReadDir(ctx context.Context, path string) ([]DirEntry, error)
}
```

两个提供者,同一契约:

- **`Backend.OpenWorkspace(ctx, WorkspaceRef) (FS, error)`** —— sessionless,面向控制面消费者(web UI、assets 恢复、goal artifact、skill 内容服务)。`WorkspaceRef` 标识 agent 的 workspace/user-data 根。本地后端返回宿主目录的薄包装(内化现有 `OpenSafeRoot` 逻辑);guest 持久盘后端由 workspace store 供给,不要求 machine 在跑。
- **`Session` 内嵌 `FS`** —— session 视角,除 workspace 外还覆盖该 session 的全部挂载(skill roots、`/opt/stella`)。核心工具走它。

设计决策:

- **流式而非字节切片**。workspace UI 的文件下载需要大文件传输;`io.ReadCloser`/`io.Reader` 对本地后端是零成本包装,对远程后端避免整文件缓冲。无 range read。<!-- 断点续传/分块的触发条件:远程 worker + 大 artifact 下载成为现实用例 -->
- **`WriteFile` 的原子性是提供者义务**(临时文件 + rename 或等价手段);半写状态不可见。
- **一致性模型:last-write-wins,无锁**。agent 与 web UI 并发写同一文件今天在宿主 FS 上就是 last-write-wins,v2 显式声明而非假装解决;`edit` 工具的 read-modify-write 竞态是现存行为,不因 v2 恶化。<!-- 乐观并发(If-Match/version)的触发条件:出现真实的并发编辑用例 -->
- **`ResolvePath`/`ResolveWritePath` 从公共契约移除**,连同 `PathResolver` 一起内化为提供者实现细节。路径校验(mount 边界、symlink 拒绝、只读挂载写保护)成为每个提供者的内部义务,由契约测试套件(§4.5)统一验证。

### 4.2 执行面:错误分类、重试语义、并发

`Exec`/`StartProcess` 签名不变,但 rev 1 未定义的语义在此固定:

- **错误分类**。`Exec` 返回 `error` 仅当**无法判定命令是否执行**(传输失败、session 死亡、路径/策略拒绝);命令执行了但退出码非零不是 error,是 `ExecResult.ExitCode`。哨兵错误进入契约:`ErrSessionDead`、`ErrPathDenied`、`ErrReadOnly`、`ErrUnsupported`。调用方据此区分"重建 session"与"把失败告诉模型"。
- **exec 是 at-most-once,禁止透明重试**。命令非幂等;`ResilientSession` 只允许在**两次调用之间**重建底层资源,一次传输中途失败的 Exec 必须把 `ErrSessionDead` 交还调用方,由上层(runner/模型)决定是否重发。这条对远程后端是正确性问题,不是风格问题。
- **并发**:契约保证同一 Session 上的并发调用安全(不 panic、不串台),但允许提供者串行化执行。现有消费方(agent turn 串行 + goal 终局检查)不需要真并发。<!-- 真并发 exec 的触发条件:出现并行工具执行需求 -->
- **输出上限**:`ExecResult` 维持全缓冲,后端强制输出字节上限(超限截断并标记)。流式 exec 不进契约;`StartProcess` 是流式席位,远程化方案见 §8。

### 4.3 能力声明:`Capabilities` 替代散落的后端知识

```go
type Capabilities struct {
        // SharedHostFS: exec 与 stellad 同文件系统(none、darwin seatbelt)。
        // 决定 PathView 是宿主路径还是规范路径,也决定内容分发是否可以 no-op。
        SharedHostFS bool
        // HostMounts: 支持把任意宿主目录挂进沙箱。远程/microVM 后端为 false,
        // 策略校验在 CreateSession 前据此拒绝带 HostPath 挂载的请求,而非静默忽略。
        HostMounts bool
        // Persistent: 支持 machine 租约与重连(§7)。
        Persistent bool
}
```

挂在 `Factory`(升级为 `Backend`)上。这是对 §1.5 的系统性回答:后端知识由后端自报,调用方按能力分派,不再有任何按名字的字符串 switch。`isolatingBackend()` 删除;策略里出现后端不支持的要素时**创建期显式失败**,不再静默降级——这是 fail-closed 原则(`web/content/docs/development/sandbox.md`)在契约层的延伸。

### 4.4 PathView 与 xberg

- **PathView**(agent 可见的规范根路径)保留 rev 1 设计,由 `Capabilities.SharedHostFS` 推导:隔离型后端展示 `/workspace`、`/user`;共享宿主 FS 的后端继续展示宿主真实路径——否则 bash 里 `pwd` 与 read 工具展示的路径不一致,模型会在两套路径间混乱。
- **xberg 进沙箱**:read 工具的文档解析从宿主 `exec.Command(xberg)` 改为 `session.Exec("xberg extract …")`。消除宿主解析不可信文件的攻击面,移除 read 工具最后一个宿主依赖。
- `ExecOptions.Cwd` 与工具传入的一切路径统一为 PathView 规范路径,不再出现宿主路径。

### 4.5 契约测试套件

新增 `pkg/sandbox/conformance`:针对 `FS` 与 `Session` 的共享测试,覆盖 mount 边界、symlink 逃逸、只读写保护、WriteFile 原子性、错误分类(§4.2 的哨兵错误)、路径视图一致性、exec env 合并、输出截断,对每个后端与每个 FS 提供者运行。这是 v2 的验收锚点——三个本地后端通过同一套测试证明重构行为不变,未来后端通过同一套测试即证明接入正确。

## 5. 内容分发:skill 如何进入非共享 FS 的沙箱

rev 1 把这个问题糊成了"后端实现细节",它不是——**db-skills 在运行期变更**(用户装/改 skill),现有机制是宿主目录只读挂载(`internal/agent/sandbox/env.go:34-46`),对共享 FS 后端天然新鲜,对 guest 磁盘后端必须显式设计:

- 控制面维护**内容 manifest**(skill 根的文件哈希树,版本号单调递增)。
- 后端在 `CreateSession` 时对账 manifest,增量同步差异内容到 guest(实现手段自选:virtiofs 只读共享、镜像层、rsync)。
- 会话中 skill 变更通过可选接口 `ContentRefresher{ RefreshContent(manifest) }` 推送(与 `EnvRefresher` 同型:下次 exec 前生效,不打断在跑进程)。
- `SharedHostFS` 后端全部 no-op。

skill 内容的宿主侧 API 服务(`skills_scoped.go`)与 agent 侧视图共享同一 manifest,消除两侧漂移。

## 6. Secret 按引用

语义与阶段以 `2026-07-04-sandbox-secrets-injection.md` 为准,本文固定两个架构位置:

1. **egress proxy / credential broker 属于执行面**,不属于 stellad。guest 只见 placeholder,worker 边界的代理在出站到 allowlist 内的 host 时注入真凭证。
2. **顺序约束:broker 先于(或伴随)worker 进程分离落地**。执行面一旦独立部署,"vault 全量注入 guest env"就从坏味道升级为跨进程明文分发事故。§9 的 Phase 4 依赖这一条。

## 7. Machine 租约模型

新领域概念 machine:可长期存续的执行环境(已装依赖、编译缓存、warm runtime),生命周期独立于 stellad 进程。workspace **不在**其中(§3)——machine 可随时销毁重建,数据不丢。

- **DB registry**(按 `schema-design.md` 设计,示意):`sandbox_machine(id, backend, user_id, agent_id, state, endpoint, created_at, last_seen_at)`。
- **状态机**:`provisioning → ready → running → stopped → lost → deleted`。`lost`(心跳超时)是显式状态而非隐式清理:reconcile 决定重连或回收,审计留痕。
- **Session 变租约**:`CreateSession` 语义变为"向后端租一台 machine"——`Persistent=false` 的后端每次新建即销毁(现状);持久后端复用绑定 machine,`Close()` 是 stop-not-delete。
- **reconcile 替代 reap**:docker 后端的孤儿清理(`EnsureReady`)推广为启动时 registry 对账——有记录则重连,无记录才回收。
- **reattach 挂在 `ResilientSession`**:重建闭包换成"按 registry 重连"即租约续期(遵守 §4.2 的 at-most-once 约束)。
- **配额与 GC**:workspace 与 machine 磁盘占用计入 per-user 配额;agent 删除触发 workspace 与绑定 machine 的回收。这不是 Phase 3 的可选项——没有 GC 的持久资源模型是存储泄漏器。

**触发条件:persistent agent workspace 成为产品需求之前不做。** 现有"10 分钟 idle 回收 + 每 session 新建"模型对当前用法自洽。

## 8. 执行面进程分离

最终形态:worker 守护进程通过 unix socket(同机)或 mTLS HTTP(独立节点)对控制面暴露 §4 契约的网络投影。rev 1 未展开的硬问题:

- **协议版本与能力协商**。"独立升级"意味着 stellad 与 worker 存在版本偏差窗口:worker API 显式版本化,握手交换 `(protocol_version, Capabilities)`,不满足最低版本拒绝服务(fail-closed)。契约的 Go 接口是内存投影,wire 协议是网络投影,conformance 套件对两者跑同一批用例。
- **`ProcessHandle` 的远程化**。`Stdin/Stdout/Stderr` 的 `io` 管道不可序列化;wire 层用多路复用流(WebSocket 或 HTTP/2 stream,同 docker exec attach 模式)承载,client 侧还原成 `ProcessHandle`。`Exec` 保持缓冲语义(§4.2 输出上限在 worker 侧执行,截断后才过网络)。
- **审计事件归控制面,由执行面发射**。每次 exec/文件写/secret 使用产生结构化事件(session、machine、agent、user 关联 ID),worker 推送、stellad 落审计存储。执行面独立后,这是"谁在沙箱里做了什么"唯一不依赖宿主同机观察的答案。
- **身份与授权**:mTLS 双向认证;worker 校验请求中的 machine 归属(machine ↔ user/agent 绑定来自 registry),防止控制面 bug 造成跨用户访问。

docker 后端(moby socket)是现成的 out-of-process 先例;microVM runtime(smolvm serve、Microsandbox)可作为原生实现。选型对比不在本文展开;架构上二者都是本契约的一个后端实现,选型不影响本设计。触发条件:需要独立升级/扩容执行面,或"secret 不入 guest"成为硬要求(先满足 §6 顺序约束)。

## 9. 迁移阶段

每阶段独立可交付、独立有收益,无大爆炸:

| Phase | 内容                                                                                                                                                                                                                             | 收益                                                                            | 触发条件                           |
| ----- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------- | ---------------------------------- |
| 1     | 契约 v2:`FS` 接口 + `Backend.OpenWorkspace` + Session 文件面 + `Capabilities`/PathView + 错误分类 + xberg 进沙箱 + conformance 套件;迁移全部消费者(read/write/edit、skill_view、workspace access 层、skills_scoped、goal runner) | 架构债主体清偿;sessionless 消费者归位;xberg 攻击面消除;新后端不再与宿主假设打架 | 立即可做,纯内部重构                |
| 2     | microVM 后端原生实现契约 v2 + §5 内容分发机制(`Available()` 门控平台能力)                                                                                                                                                        | bwrap 之外的强隔离选项;验证契约对非宿主后端成立                                 | Phase 1 完成                       |
| 3     | machine registry + 租约 + 状态机 + reconcile + 配额/GC                                                                                                                                                                           | persistent agent workspace;stellad 重启后 machine 幸存                          | 产品需求明确                       |
| 4     | worker 进程分离(版本化协议、流式 attach、mTLS、审计事件)+ credential broker/egress proxy                                                                                                                                         | 执行面独立升级/扩容;secret 不入 guest;跨机审计                                  | 部署形态或安全要求驱动;broker 先行 |

Phase 1 的验收:`mise run format && build && test` 通过;conformance 套件对 local/docker/none 全绿;`mise run system-test` 通过;行为差异仅限 xberg 执行位置(none/darwin 后端下从宿主 PATH 改为沙箱 exec——同机同文件系统,用户不可见)。

## 10. 明确不做与已知代价

- **任务续跑不在本架构内**。runner、agent loop、对话状态在 stellad 内存中;machine 幸存保住的是 warm runtime,workspace 幸存保住的是数据,都不是在跑的 turn。任务续跑需要 agent loop 的 turn 级可恢复性,是正交的另一项工程。
- **文件并发不引入锁**。last-write-wins 显式化(§4.1),不为假想的协同编辑场景付锁协议的复杂度。
- **每次文件操作多一跳间接**。本地后端是进程内函数调用,成本可忽略;远程后端付网络往返,那正是换来的能力。
- **none 后端的"过度抽象"观感**。它实现 `FS` 就是包一层 `os.*`(约五十行),接口通用、实现具体的正确分配。
- **`StartProcess` 目前无生产调用方**(文档声称 MCP stdio 使用它,代码中无调用)。契约保留它作为流式席位(§8 已给出远程化路径),Phase 1 不为它新增消费者;若两个 Phase 后仍无调用方,议程删除。

## 11. 待验证的开放问题

1. microVM 候选(smolvm)是否暴露宿主目录共享(virtiofs)——影响 Phase 2 内容分发选 "共享挂载" 还是 "manifest 同步",不影响契约。
2. `assets.Restore` 懒恢复(`workspace.go:235`)在 `OpenWorkspace` 路径上的归属:恢复逻辑上移到 workspace store 内,还是保留在 access 层——Phase 1 定,倾向前者(store 对消费者隐藏"文件曾被换出"这个错误,define errors out of existence)。
3. 远程文件 API 的大文件路径(web UI 下载 artifact)是否需要分块/断点——Phase 4 前评估,`io.Reader` 已够第一版。
4. Windows:local 后端目前不支持;Phase 2+ 的 microVM 后端需明确 Windows 门控(WHP/WSL2)。
5. per-user 配额的计量点(workspace store 统一计量 vs 后端各自上报)——Phase 3 定。
