# Sandbox 架构 v2:控制面/执行面分离与 Session 契约完备化

- **日期**: 2026-08-01
- **状态**: Draft,待评审
- **范围**: `pkg/sandbox` Session 契约、`internal/agent/sandbox` 核心工具与 skill/workspace 视图、machine 生命周期模型、执行面进程分离的立项路线。不含 agent loop 可恢复性(见 §9)。
- **关联**: `docs/design/2026-07-04-sandbox-secrets-injection.md`(secret 注入语义以该文档为准,本文只定义它在新架构中的位置)

## 1. 问题:巧合耦合

`Session` 接口(`pkg/sandbox/session.go:12`)名义上是后端无关的沙箱契约,实际依赖一个未声明的前提:**沙箱与 stellad 共享宿主文件系统**。这个前提对现有三个后端(local/bwrap、docker bind mount、none)碰巧成立,但它是巧合,不是设计。泄漏点:

1. **路径解析返回宿主路径,文件 I/O 在契约外**。接口注释明示 "use os.* with the resolved path"(`pkg/sandbox/session.go:23-28`);`read`/`write`/`edit` 三个核心工具解析出宿主路径后直接 `os.ReadFile`/`os.WriteFile`(`internal/agent/sandbox/read.go:95`、`write.go:55`、`edit.go:56,70`)。`Session` 上没有任何文件方法——这是有意设计(`web/content/docs/development/sandbox.md` "File I/O is runner-owned"),v2 要推翻的正是这条。
2. **xberg 在宿主上解析沙箱文件**。`read` 对二进制文档 shell out 到宿主的 xberg 二进制(`internal/agent/sandbox/read.go:239-253`)。文档解析器吃 agent 写入的不可信文件,这是独立于路径耦合的攻击面。
3. **Mount 语义即宿主 bind mount**。`Mount{HostPath, SandboxPath}` 和 `FilesystemPolicy`(`pkg/sandbox/policy.go:74-93`)把"宿主目录是数据真身"写进了策略类型。
4. **后端身份靠字符串 switch 判断**。`isolatingBackend()` 对后端名做字符串比较来决定 agent 看到的路径视图(`internal/agent/sandbox/skill_view.go:83-86`),新增后端必须记得来这里登记,否则 workspace 路径展示直接错。
5. **执行状态是进程内的、一次性的**。session 缓存在内存(`internal/agent/runtime/runner_cache.go`),10 分钟 idle 回收(`internal/agent/pool_manager.go:179`),DB 无任何沙箱记录;docker 后端启动时主动 reap 孤儿容器(`plugins/sandbox/docker/session.go:142-156`)——系统假设"遗留执行状态即垃圾"。

后果:任何"磁盘不在宿主上"的后端(microVM、远程 worker 节点、Kubernetes runtime)都无法在不改契约的情况下接入;执行面无法独立于 stellad 升级、重启、扩容;宿主承担了本应隔离在沙箱内的解析工作。

## 2. 目标形态

```text
stellad(控制面)
  用户 / Agent / 对话 / Goal / Vault / 审计 / 策略决策
        │
        │  窄契约:lifecycle · exec · fs · stream · secret-by-reference
        ▼
Sandbox Worker(执行面)
  ├─ machine registry + reconcile(DB 持久,重启后重连)
  ├─ credential broker / egress proxy(真 secret 只活在这一层)
  └─ machines(bwrap | microVM | container —— worker 内部实现细节)
```

**核心原则:stellad 把每个沙箱都当远程对待,本地是远程的特例。** 现在是反的——远程被当成本地的特例,所以宿主路径假设泄漏到工具层、视图层和策略类型里。

由此推出四条子原则:

| #  | 原则                                                                 | 替换的现状                                 |
| -- | -------------------------------------------------------------------- | ------------------------------------------ |
| P1 | 契约完备:所有介质(exec、文件、流)穿过 `Session`,调用方不接触宿主路径 | 工具层 `os.*` + `ResolvePath` 返回宿主路径 |
| P2 | Workspace 归执行面所有,控制面通过契约访问                            | 宿主目录是真身,沙箱挂载它                  |
| P3 | Secret 按引用传递,真值只在执行面边界注入                             | `Policy.Env` 明文进 guest 进程 env         |
| P4 | Machine 是持久资源,Session 是租约                                    | session 拥有沙箱,孤儿即垃圾                |

## 3. 契约 v2:Session 文件 API(P1)

### 3.1 接口

```go
type Session interface {
        // Lifecycle(不变)
        Policy() Policy
        Close() error
        Alive() bool
        Done() <-chan struct{}

        // Process surface(不变)
        Exec(ctx context.Context, command string, opts ExecOptions) (ExecResult, error)
        StartProcess(ctx context.Context, req ProcessRequest) (ProcessHandle, error)

        // File surface(新增;path 为沙箱视角的规范路径)
        ReadFile(ctx context.Context, path string) (io.ReadCloser, error)
        WriteFile(ctx context.Context, path string, r io.Reader, perm fs.FileMode) error
        Stat(ctx context.Context, path string) (FileInfo, error)
        ReadDir(ctx context.Context, path string) ([]DirEntry, error)

        // View(新增,替代 skill_view 的字符串 switch,见 §3.3)
        PathView() PathView

        WorkingDir() string
}
```

设计决策:

- **流式而非字节切片**。已知调用方里 workspace UI 的文件下载需要大文件传输;`io.ReadCloser`/`io.Reader` 对本地后端是零成本包装,对远程后端避免整文件缓冲。read 工具自己维持现有的分页/截断逻辑。
  <!-- 无 range read;需要断点续传时再加 offset 参数,触发条件:远程 worker + 大 artifact 下载 -->
- **`ResolvePath`/`ResolveWritePath` 从公共契约移除**,连同 `PathResolver` 一起内化为本地后端的实现细节。路径校验(mount 边界、symlink 拒绝、只读挂载写保护)成为每个后端 `ReadFile`/`WriteFile` 的内部义务——契约测试套件(§3.4)负责验证所有后端执行同样的规则。
- **`EnvRefresher`、`ResilientSession` 装饰器不变**。`ResilientSession` 需要代理新增的四个文件方法,机械工作。

### 3.2 xberg 进沙箱

read 工具的文档解析从宿主 `exec.Command(xberg)` 改为 `session.Exec("xberg extract …")`。xberg 已经通过 mise 分发到沙箱工具链,改动是调用位置。收益双重:消除宿主解析不可信文件的攻击面;移除 read 工具最后一个宿主依赖。

### 3.3 PathView 替代 isolatingBackend

`skill_view.go` 的字符串 switch 之所以存在,是因为"agent 看到什么路径"这个知识放错了地方——它属于后端,不属于调用方。v2 让后端自报:

```go
// PathView 描述 agent 在沙箱内看到的规范根路径。
// 隔离型后端(docker、linux bwrap、microVM)返回 /workspace、/user;
// 共享宿主文件系统的后端(none、darwin seatbelt)返回宿主真实路径,
// 保证 read/write 展示的路径与 bash 里 pwd 输出一致。
type PathView struct {
        WorkspaceRoot string
        UserDataRoot  string
}
```

`WorkspaceViewFor`/`UserDataViewFor` 改为读 `session.PathView()`,`isolatingBackend()` 删除。新增后端不再需要在调用方登记自己。

注意:**非隔离后端继续展示宿主路径是刻意的**。若强行给 none 后端展示 `/workspace` 规范路径,agent 在 bash 里 `pwd` 看到的却是宿主路径,模型会在两套路径间混乱。规范路径只在"exec 视角与文件视角一致"的后端启用,这正是 PathView 由后端自报的原因。

### 3.4 契约测试套件

新增 `pkg/sandbox/conformance`(或等价物):一套针对 `Session` 接口的共享测试,覆盖 mount 边界、symlink 逃逸、只读写保护、路径视图一致性、exec env 合并,对每个后端实例运行。这是 v2 的验收锚点——"三个本地后端通过同一套契约测试"证明重构行为不变,后续 microVM 后端通过同一套测试即证明接入正确。

## 4. Workspace 归属(P2)

契约 v2 落地后,"workspace 在哪"成为后端私有知识:本地后端映射到宿主目录(现状不变),microVM 后端可以放在 guest 磁盘,远程 worker 放在节点本地盘。控制面所有消费者——四个核心工具、skill 分发视图、goal 终局检查(`internal/goal/runner.go:57-98`)、web UI 的 workspace 浏览——统一走 `Session` 文件 API。

`FilesystemPolicy`/`Mount` 保留为**对本地后端的实现指令**,但从"数据真身声明"降格为"后端如何组装文件系统的提示";非宿主后端可以忽略 `HostPath` 而以 skill/workspace 内容同步替代(内容如何进入 guest 是后端实现细节,如 virtiofs、镜像层、启动时 rsync)。

## 5. Secret 按引用(P3)

语义与阶段以 `2026-07-04-sandbox-secrets-injection.md` 为准,本文只固定两个架构位置:

1. **egress proxy / credential broker 属于执行面**,不属于 stellad。guest 只见 placeholder,worker 边界的代理在出站到 allowlist 内的 host 时注入真凭证。
2. **顺序约束:broker 先于(或伴随)worker 进程分离落地**。执行面一旦独立部署,"vault 全量注入 guest env"就从坏味道升级为跨进程明文分发事故。§8 的 Phase 4 依赖这一条。

## 6. Machine 租约模型(P4)

新领域概念 machine:一台可长期存续的执行环境(持久 workspace、已装依赖、编译缓存),生命周期独立于 stellad 进程。

- **DB registry**(按 `schema-design.md` 设计,示意):`sandbox_machine(id, backend, user_id, agent_id, state, endpoint, created_at, last_seen_at)`。
- **Session 变租约**:`CreateSession` 语义变为"向后端租一台 machine"——临时后端每次新建即销毁(现状),持久后端复用绑定的 machine;`Close()` 对持久 machine 是 stop-not-delete。
- **reconcile 替代 reap**:docker 后端的孤儿容器清理(`EnsureReady`)推广为启动时 registry 对账——registry 有记录则重连,无记录才回收。
- **reattach 挂在 `ResilientSession`**:它已建模"底层资源死亡后透明重建"(`pkg/sandbox/resilient.go:19`),把重建闭包换成"按 registry 重连"即租约续期。

**明确的触发条件:persistent agent workspace 成为产品需求之前不做。** 现有"10 分钟 idle 回收 + 每 session 新建"模型对当前用法自洽,machine registry 是为长期 runtime(microVM warm pool、独立 worker)预留的正确形状,不是当前缺陷的修复。

## 7. 执行面进程分离

最终形态:worker 守护进程通过 unix socket(同机)或 mTLS HTTP(独立节点)对控制面暴露与 `Session` 契约同构的 API。docker 后端(moby socket)是现成的 out-of-process 先例;microVM runtime(smolvm serve、Microsandbox)可作为原生实现。触发条件:需要独立升级/扩容执行面,或"secret 不入 guest"成为硬要求(此时先满足 §5 的顺序约束)。

选型对比(smolvm vs Microsandbox 的完整分析见评审记录,结论:Microsandbox 适合先做 microVM PoC,smolvm 的持久 Machine/Fork 模型在 Phase 3+ 才兑现价值)不在本文展开;架构上二者都是 §3 契约的一个后端实现,选型不影响本设计。

## 8. 迁移阶段

每阶段独立可交付、独立有收益,无大爆炸:

| Phase | 内容                                                                                                                                                | 收益                                                         | 触发条件                           |
| ----- | --------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------ | ---------------------------------- |
| 1     | 契约 v2:Session 文件 API + PathView + xberg 进沙箱 + 三个本地后端平凡实现 + 契约测试套件;工具层、skill_view、workspace UI、goal runner 改为契约调用 | 架构债主体清偿;xberg 攻击面消除;新后端接入不再与宿主假设打架 | 立即可做,纯内部重构                |
| 2     | microVM 后端原生实现契约 v2(`Available()` 门控平台能力)                                                                                             | bwrap 之外的强隔离选项;验证契约对非宿主后端成立              | Phase 1 完成                       |
| 3     | machine registry + 租约 + reconcile                                                                                                                 | persistent agent workspace;stellad 重启后 machine 幸存       | 产品需求明确                       |
| 4     | worker 进程分离 + credential broker/egress proxy                                                                                                    | 执行面独立升级/扩容;secret 不入 guest                        | 部署形态或安全要求驱动;broker 先行 |

Phase 1 的验收:`mise run format && build && test` 通过;契约测试套件对 local/docker/none 全绿;`mise run system-test` 通过;行为差异仅限 xberg 执行位置(none/darwin 后端下 xberg 从宿主 PATH 改为沙箱 exec——同机同文件系统,用户不可见)。

## 9. 明确不做与已知代价

- **任务续跑不在本架构内**。runner、agent loop、对话状态在 stellad 内存中;machine 幸存保住的是 warm workspace,不是在跑的 turn。任务续跑需要 agent loop 的 turn 级可恢复性,是正交的另一项工程,不要把它当成本架构的卖点。
- **每次文件操作多一跳间接**。本地后端是进程内函数调用,成本可忽略;远程后端才真正付网络往返,而那正是换来的能力。
- **none 后端的"过度抽象"观感**。它实现文件 API 就是包一层 `os.*`(约五十行),这是"接口通用、实现具体"的正确分配,不是过度设计。
- **`StartProcess` 目前无生产调用方**(文档声称 MCP stdio 使用它,代码中无调用)。契约 v2 保留它作为未来流式进程的席位,但 Phase 1 不为它新增消费者;若两个 Phase 后仍无调用方,应议程删除。

## 10. 待验证的开放问题

1. microVM 候选(smolvm)是否暴露宿主目录共享(virtiofs)——影响 Phase 2 是"内容同步"还是"共享挂载"两条实现路线的选择,不影响契约。
2. 远程文件 API 的大文件路径(web UI 下载 artifact)是否需要分块/断点——Phase 4 前评估,契约预留 `io.Reader` 已够第一版。
3. Windows:local 后端目前不支持,none 后端共享宿主文件系统,契约 v2 对二者无新增约束,但 Phase 2+ 的 microVM 后端需明确 Windows 门控(WHP/WSL2)。
