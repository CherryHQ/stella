---
title: 沙箱后端抽象
---

> 本节面向为 Stella 贡献代码的开发者。选择和配置沙箱后端请参阅[沙箱指南](/docs/guides/sandbox)。

## 核心模型

沙箱抽象的目的是使 runner 代码、插件配置和工具执行不依赖于具体的后端类型。执行总是通过 runner 选中的活动后端进行。

- `pkg/sandbox.Policy` — 不可变的、后端无关的执行策略（进程可见的文件系统根目录、工作目录、网络模式、环境变量、超时）
- `pkg/sandbox.Session` — 一个 Session 计算资源的执行与文件能力；runner 句柄从代次 owner 借用其生命周期
- `pkg/sandbox.FileAccess` — 由 `Session.Files` 返回的中介文件 capability；调用方与命令使用同一套进程可见坐标，且永远不会获得 provider backing path

后端标识保留在 runner 和面向 runner 的 sandbox 包内部。插件包不导入 `internal/agent/sandbox`。

## Session 接口

`pkg/sandbox.Session` 暴露 8 个方法：

| 方法                                                       | 描述                                     |
| ---------------------------------------------------------- | ---------------------------------------- |
| `Policy() Policy`                                          | 返回会话创建时使用的不可变策略           |
| `Exec(ctx, command, ExecOptions) (ExecResult, error)`      | 运行命令并等待结果                       |
| `StartProcess(ctx, ProcessRequest) (ProcessHandle, error)` | 启动带 stdio 句柄的长期运行进程          |
| `Files() FileAccess`                                       | 返回对已授权进程可见数据 root 的中介访问 |
| `WorkingDir() string`                                      | 返回沙箱内的逻辑工作目录                 |
| `Close() error`                                            | 关闭会话并释放资源                       |
| `Alive() bool`                                             | 报告会话是否仍然活跃                     |
| `Done() <-chan struct{}`                                   | 会话终止时关闭的 channel                 |

`FileAccess` 提供 prompt 构建与核心 `view_image` 工具所需的有界操作，以及当前选中资源文件在发布时精确、no-replace、disposable 的投影。路径相对于 `WorkingDir`，或使用进程视图中的绝对路径。公开的 `Policy`、`Session` 与 `FileAccess` contract 不包含宿主机 mount source、路径 resolver 或路径转换结果。

每个 backend 都在 provider 内部把公开的进程 root 绑定到物理 mount plan。文件操作使用 Session 创建时固定的目录 capability，执行只读 root 约束，并对逃逸或跨 mount symlink fail closed。Provider 的进程准备代码可以读取自己的私有映射，但上层无法先取得物理路径，再用 `os.*` 绕过 capability。

## 本地 workspace 所有权

Phase 1 仅支持一个副本和一个可信 POSIX `STELLA_HOME`。PostgreSQL owner row 是身份和授权 authority；`STELLA_HOME` 下的确定性路径是布局与字节 authority。`internal/platform/home.WorkspaceManager` 是唯一生产物化器：只有确认 user、group 和 Agent owner 存活后才创建缺失目录，并拒绝 symlink、非目录、不安全 ID 和可信根替换。原始 ID 相同的用户和群组使用不同路径。

用户或群组运行使用已授权 `WorkspaceView` 返回的精确 `AgentRoot` 和 `DataRoot`。隔离型 backend 会把这些 root 以读写方式挂载；显式选择的 `none` backend 仍是 trusted-host execution，不提供进程级文件系统隔离。无用户运行保持 disposable scratch 语义，不获得 principal mount。群组 Agent Home 的 Skill materialization 不含 user 或 `user_agent` scope：它不会把群组数据变成某个用户的 `user_agent` Skill。

在 Session 执行之外，`WorkspaceManager.OpenRoot` 生成有 scope 的只读或读写操作 capability。类型化 root component 以 no-follow traversal 物化；操作使用 inode-pinned `os.Root`，因此 root 内的相对 symlink 可用，绝对或逃逸 symlink 会 fail closed。这不是 `Session` filesystem transport：Stella 不提供 `stella-fs` 或 Docker exec filesystem RPC。下游文件 consumer 将由后续变更分别迁移。

显式破坏性删除 user、group 或 Agent 时，会先 fence 本地执行，再删除数据库 owner。文件和 inode 保留，但后续 `WorkspaceView` 因 owner 不存在而失败。`agents/{id}` 的任意文件系统条目都会保留全局 Agent ID。这些保证仅适用于可信宿主和单副本。多副本部署保持相同应用模型，但还需要一个强一致 shared POSIX namespace 与 PostgreSQL generation/lease fencing；S3 不是 live Workspace authority。

## 当前架构

### 会话所有权

`GenerationStore` 持有计算资源，其权威是 PostgreSQL 中的 Session 代次，以及与 `AgentRun` 共用的不可变 executor boot。代次只标识可丢弃的计算资源，不用于定位 Workspace 文件。部分唯一索引限制每个 Session 只能有一代未销毁的资源；只有证明上一代资源已停止，才能创建下一代。

runner 句柄借用该资源。runner 闲置 10 分钟后的普通回收只释放借用，后续 runner 可以复用同一健康代次和不可变策略；逐轮环境替换仍负责更新凭据。每个进程最多保留 1024 代资源。如果无法安全回收任何闲置资源，新增资源会得到明确的容量错误，不会静默淘汰已有健康原生资源。

用户 CLI 的私有 preparation Session 属于同一代次的辅助资源，每次尝试都有独立目录并在执行前登记。每代最多保留 64 个尚无缺失证明的 preparation，达到上限时，新的安装会在创建资源之前被拒绝。安装结果已知时可发布不可变选择；原生终止尚无证明时保留辅助资源和目录。执行结果不明会 fence 整个代次，替换或销毁要求主资源及全部辅助资源都已证明不存在。Docker toolcache helper 仍由独立的共享缓存 owner 管理。

旧代次的操作在进入后端前被拒绝。计算操作结果不确定时封锁该代次，不得在替代资源上重放；能够证明尚未开始执行的失败，不会封锁健康资源。Workspace/API 文件访问保持独立。

### 后端解析

runner 会从 `STELLA_SANDBOX_BACKEND` 解析部署时后端，并通过注入的已编译后端 registry 分派。生产部署使用 `docker`、`local` 或 `none`；Harbor 评测 harness 还会接入仅供评测使用的 `bridge` 后端。

`kubernetes` backend 为本地/dev 提供同节点、共享 PVC 的 Pod 执行，见 [Kubernetes](../admin/kubernetes.zh.md)。

### 执行时中介

所有必须遵守沙箱策略的本地执行路径都通过活动 runner 会话进行中介：

- 核心 `bash` 工具通过 runner 拥有的会话使用 `Session.Exec`
- 核心 `view_image` 工具与活动 prompt context 读取使用 `Session.Files`
- 当前选中的 Skill 和包文件通过 `FileAccess.ProjectFiles` 复制到精确、no-replace 的 Session 投影；已存在但内容冲突的 tree 会 fail closed
- 插件工具接收 `ToolContext.Runtime`，这是活动会话上的 `pkg/plugins.ToolRuntime` 适配器
- 技能和代理预设加载在代理会话内运行时使用 `ToolRuntime`

读取文件的核心工具每次调用只选择一个 `FileView`。其中的策略环境、工作目录与 `FileAccess` 来自同一个 resilient generation，因此路径展开不会在中途静默切换 backing tree。跨越该边界的 provider 错误只标识逻辑进程 mount，不暴露物理 source path。

资源投影会原子发布，并在每次 load 时校验，但它不是针对同一用户身份运行命令的独立隔离边界。此类命令可能与校验并发，或在校验后修改 disposable tree。只要 load 观察到不一致，就会 fail closed，而不会替换该路径。只有后端能够确认没有所属执行仍在使用临时文件时，才移除其 backing。Docker 的旧版启动清理会跳过已由 generation store 管理的资源。

### 长期运行进程

`Session.StartProcess` 可供后端拥有的长期运行进程使用。MCP 插件连接仍是远程 HTTP
传输；这个接口不会增加另一条本地 MCP 执行路径。

### 非 runner 文件系统访问

某些代码路径需要在没有已注入运行时的情况下访问本地文件系统，例如活动代理运行之外的提示渲染或元数据发现。

runner 外的项目提示上下文与项目级 Skill 读取会解析精确的用户、Agent 与项目，打开只读 Agent Home capability，复制有界逻辑内容，并在提示或 Skill 处理前关闭 capability。逻辑项目 `base_dir` 不会被当作进程工作目录。其他可信的非项目元数据发现仍可使用 local runtime。这些是有意为之的非 runner 路径，而不是沙箱化工具执行的回退。

### 显式例外边界

远程 MCP HTTP/SSE/StreamableHTTP 传输目前被视为独立的信任边界。

- 远程传输拨号目前**不**由 `ToolRuntime` 中介
- 此例外被显式跟踪为 `EX-009`，并记录为 `runtime.exception_path`

## 拒绝失败行为

Stella 优先选择显式拒绝而非静默降级：

- 会话创建时 Docker 不可用 → runner 启动失败
- 不支持的策略 → `PolicyCompatibilityError`，runner 启动失败
- 直接非中介的插件 exec → 拒绝失败
- 远程 MCP HTTP/SSE/StreamableHTTP → 显式例外，而非隐式沙箱绕过

## 验证

该抽象由以下测试覆盖：

- 会话/宿主机契约测试
- 策略兼容性测试
- 核心工具一致性测试
- Docker 后端集成测试
- 已迁移运行时路径的静态绕过回归保护

## 本地运行 Docker 后端

`mise run dev:docker` 一条命令拉起整套栈，对齐生产的 `docker-compose.yml`：`stellad` 跑在**容器内**，docker 沙箱后端走 **volume 模式**（`STELLA_SANDBOX_BACKEND=docker`、`STELLA_DOCKER_SANDBOX_MODE=volume`、`STELLA_HOME_VOLUME=stella-data`），外加一个 `otel-lgtm` 边车。它会构建本地镜像（`docker:build` → `stella:latest`、`sandbox:docker:build` → `stella-sandbox:dev`）、按需新建命名卷，并确保 `~/.stella-dev/.env` 里有 dev vault key。它跑的是和 prod 同一份 `docker-compose.yml`，只是导出 `STELLA_IMAGE=stella:latest`，从而用本地构建而非发布镜像。

容器内 Go 服务器在 `localhost:25688` 提供其烤进镜像的内嵌 SPA（见 `web/embed.go`），Grafana 在 `localhost:13413`。

用 `docker compose down` 停掉整套栈。

sandbox 镜像包含发行版自带的 mise 工具链和 builtin CLI 文件。运行时从
`system`、`system_agent`、`user`、`user_agent` 四层解析完整包文件，再只物化选中的
条目。Docker preparation 按一个解析后的 image ID 和完整选择身份做缓存键。user 和
user-agent 安装留在自己的沙箱目录，并在 `PATH` 中优先。不会使用宿主机 `_builtin.toml`、
manifest 权限面，也不会把宿主机平台的安装作为 Docker 回退。

## 沙箱中的文件资源

运行时在创建进程环境前捕获文件资源。四种作用域是 `system`、`system_agent`、`user`
和 `user_agent`，项目 Skill 从项目目录读取。选中的完整包决定其中的 Skill、CLI、环境
绑定和 MCP 声明。复制包独立存在，不会接收来源后续编辑。

进程只看到当前选中的条目和运行它所需的会话坐标。Skill 或包文件不是第二个沙箱边界。
删除文件会移除后续选择，OAuth Disconnect 则单独在本地撤销匹配的已存 grant、关闭连接并阻止
迟到刷新；不承诺远端 provider 一定撤权。运行时目录只是观测，后续 turn 可以复用或刷新，
MCP 连接会随 Session 关闭。

发行版 builtin Skill bundle 保持不可变，local 和隔离型后端在执行坐标投影它。bundle
是发行物，不是可变的资源根。操作员命令语法使用 `stellad system-bundle
--help` 查询。bundle 工具变化时，使用匹配的 Stella 版本重建自定义 Docker 镜像。

## 资源清理和进程边界

数据库封锁先于物理清理。清理失败、控制面不可达、资源身份不完整，或封锁写入失败，都不能授权创建替代资源。资源 controller 根据部署配置重建，因此恢复不依赖原进程中的 Session 句柄。

Docker 将创建前取得的 daemon 身份与完整、不可变的 container ID 绑定；另一个 daemon 上的查询不能证明旧资源缺失。Kubernetes 将部署 PVC UID、namespace 与 Pod UID 绑定。Pod 对象消失本身不能证明执行已停止，网络分区中的节点或强制删除都可能留下运行中的进程；后端在观察到精确 Pod 的终态前保留自己的 finalizer。

local 和 `none` Session 启动独立的原生进程。原始 raw Session 从未启动进程且已关闭时可以证明资源不存在；Linux local 在所有 bwrap PID 命名空间 owner 都已回收后也可以提供证明。关闭主进程、观察到空进程列表或找不到 PID，都不能证明脱离进程组的后代已停止。无法取得充分证明，或重启后丢失原始 raw 观察器时，代次保持 unknown，临时文件继续保留。bridge 资源由外部评测 harness 持有，关闭 Stella 连接不能证明资源已销毁。

运维恢复可以查看代次、让 controller 核实并清理，或为 unknown 资源记录明确的人工缺失证明。人工确认必须指定精确的 Session、代次、owner boot、审计原因和显式确认。旧 owner 必须已 drained 或超过 30 秒未心跳，且该 Session 不得仍有 running Run。具体语法见 `stellad sandbox --help` 及各子命令帮助。这只改变计算资源的可创建状态，不会恢复旧 Run，也不会删除 Workspace 文件。

部署仍限制为单副本。渠道领导权、持久发布和恢复、远程实时订阅，以及共享存储就绪，仍是独立的多副本启用前提。

## 添加新后端

每个新沙箱后端需要在以下所有位置进行修改——遗漏任何一处都会导致运行时错误：

| 步骤 | 文件                                      | 操作                                                                                             |
| ---- | ----------------------------------------- | ------------------------------------------------------------------------------------------------ |
| 1    | `internal/platform/config/sandbox.go`     | 添加 `SandboxBackend<Name> = "<name>"` 常量                                                      |
| 2    | `internal/platform/config/sandbox_env.go` | 在 `ActiveSandboxBackend` 的 `STELLA_SANDBOX_BACKEND` switch 中接受该名称                        |
| 3    | `plugins/sandbox/<name>/session.go`       | 实现 `sandbox.Factory` 和 `sandbox.Session`                                                      |
| 4    | `cmd/stellad/setup_sandboxes.go`          | 注册 `sandbox.BackendDefinition` adapter，并提供所有由进程持有的依赖                             |
| 5    | 测试                                      | 覆盖后端实现和 composition-root adapter，并确保 `internal/boundary_test.go` 中的依赖守卫保持通过 |
| 6    | 文档                                      | 更新[沙箱指南](/docs/guides/sandbox)和本文件                                                     |

## 相关文档

- [沙箱指南](/docs/guides/sandbox) — 选择和配置后端
- [架构](/docs/development/architecture)
