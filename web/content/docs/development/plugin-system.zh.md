---
title: 插件系统
---

Agent Plugins 通过类型化资源作用域中的一棵完整包文件树组合 Skills、CLI 依赖、环境绑定和 MCP 服务。
编译进程序的 Stella Native
Plugins 使用可信 Go 注册、部署级配置，以及管理员设置的逐 Agent 禁用策略。

## Native 管理

管理员在 **管理控制台 > 集成 > 原生能力** 中管理开关。全局开关影响所有 Agent，
逐 Agent 禁用只限制选中的 Agent。移除逐 Agent 禁用后，全局开关仍需开启才能访问。
用户设置不能解除这些限制。

`NativePolicy` 在一次读取中检查全局 `plugin` 行和 `native_agent_deny`。
全局行不存在时，只使用可信 Native 注册表的默认值。未注册身份或策略读取失败时，
拒绝新的执行准入。Native hooks、频道 listener 和后台任务共用此策略；
频道实例继续保留自己的开关及凭据。

Native 工具使用静态导出名称，将权限保存在 `tool_override.tool_name`。
现有四层工具限制同时应用于发现和每次调用，已构建的 runner 也会重新检查。
Native 工具身份不依赖 Agent Plugin 定义外键。导入流程与运行时共用显式 Native
注册表，Native 全局配置留在现有存储中。

Native 写入经过 runner 准入屏障。提交成功或提交结果未知时，都会回收旧 runner，
并重新协调频道 listener，避免一次报错响应留下旧权限。
Native 管理方法要求可信管理员身份，非 HTTP 调用也不能绕过；逐 Agent 限制还需通过
Agent 授权服务。OAuth access token 无法访问管理 API。Host 通过 `ConfigStore.Get`
读取配置，不再提供绕过管理服务的配置写入或开关方法。

## Native Go 契约

Native runtime 实现 `Apply`、`Stop` 和 `Snapshot`，运行时查询使用 `Get`。
频道协调通过 `ReconcileChannel` 接收已提交的频道 ID，注册信息使用
`ManagedChannelPluginRegistration.Info`。这些接口不保留 `Start`、`Reconcile`、
`Status`、`Lookup`、`ApplyChannel` 或 `Meta` 兼容别名；仓库外编译的 Native 插件
需要更新对应调用后重新构建。

`PluginInfo.Capabilities` 声明注册特征，由 Host 对照实际注册验证。
`RequiredCapabilities` 独立声明宿主端口，继续执行默认拒绝的权限检查。

## 文件资源模型

Agent 资源是四个有明确类型的资源根中的普通文件。资源根由可信的组合层提供，
通过 Home capability 打开；资源文件不能自行声明所有者。

| 作用域         | 所有者           | 典型资源根          |
| -------------- | ---------------- | ------------------- |
| `system`       | 部署             | 系统资源            |
| `system_agent` | 一个 Agent       | 该 Agent 的系统资源 |
| `user`         | 一个用户         | 该用户的资源        |
| `user_agent`   | 一个用户和 Agent | 该用户的 Agent 资源 |

项目还可以提供 `.agents/skills/` 资源，但它只参与 Skill 选择。解析器只在四个资源根中按
`user_agent > user > system_agent > system` 选择完整包候选。相同名称的
候选会整体替换更宽作用域的包，不会把落选包的文件、CLI 声明、环境绑定、Skill
或 MCP 声明合并进来。独立 Skill 和 MCP 文件是独立资源，不会因为名称相同而成为
包成员。

`settings.json` 是策略文件，不是包内容。它只保存 `disabled`、只有管理员能设置的
`forbidden` 和 `disabled_tools`。被停用或解析失败的胜者会遮蔽继承资源，个人设置
不能解除管理员禁止项。Native 能力使用独立注册表和策略，同名包不会因此获得 Native
工具。

文件 API 和 Web UI 编辑的就是运行时发现读取的同一批字节。复制完整包会在目标所有者
下创建新的独立资源，没有父级链接，来源后续编辑不会升级副本。包编辑替换完整目录。
独立 MCP 声明使用 `mcp/<name>.json`，公开端点和凭据引用写在文件中，秘密值保存在
独立的授权存储中。

## Turn 捕获和生命周期

接纳 turn 时，运行时捕获选中的文件，并用同一份固定视图生成提示词、搜索和加载 Skill、
选择 CLI、绑定环境以及发现 MCP。turn 中途的编辑在下一轮生效，本轮不会重新打开后续
文件版本。捕获身份仍匹配时，已有连接和未变化的 CLI 产物可以复用。

API 修改使用摘要检测过期的 UI 编辑。Shell 直接写文件仍是普通的非事务编辑。Home
所有者锁只防止该所有者目录被并发删除，不是任意 Shell 写入的通用 compare-and-swap。

文件删除、策略停用和凭据撤销是三个独立动作。删除文件会移除声明，使它不再参与后续
选择，但不会撤销 OAuth grant。MCP 的 **Disconnect** 会在本地撤销匹配的已存 grant、
轮换 generation、关闭匹配连接，并阻止迟到回调和刷新；不承诺远端 provider 一定撤权。
端点或认证身份变化会产生新的目标，不能复用旧 grant。
Native 账号和 Agent 撤权继续由各自的 Native 或 Agent 策略负责。

运行时不会在线垃圾回收包字节、Skill 历史、资源派生快照或安装缓存。MCP 连接属于
Session，会随 Session 关闭；目录观测可以保留供后续发现，但永远不是权威。资源清理
需要明确且经过验证的维护操作。TTL 或猜测 PID 不能替代进程及其后代已经停止的证据。

## CLI 和 Skill 资源

包可以包含二进制文件、Skill、环境声明、提示词片段和 MCP 声明。包解析直接读取本轮
捕获的文件树，不存在可以覆盖文件的生成运行时目录。包消费者只准备当前轮次选中的条目，
可以复用匹配的已安装产物。共享安装缓存要等进程清理安全后才处理，不能成为第二份包权威。

Builtin 和发行版 Skill 是带有可信所有权的发布文件。用户 frontmatter 不能声明发行版
所有者。包内和独立 Skill 仍执行相同的 frontmatter 与大小检查，包准备失败时本轮保留
masked 状态，不会恢复更低优先级的候选。

Channel、email、recally 和 scheduler 等 Native 集成属于独立系统能力。它们的编译注册、
账号凭据和 Agent 策略留在各自领域。停用包只隐藏文件资源，不会停用同名 Native 集成。

## Sandbox 进程边界

local backend 会执行配置的文件系统和网络策略，但 leader 关闭不能证明脱离进程组的后代
已经停止。`none` backend 不提供可靠的进程隔离。因此正常关闭 turn 不足以证明可以删除
资源字节或派生缓存。Docker 负责清理它创建的 Session 资源；包和 MCP 资源的保留独立于
Session 生命周期。

## Channel 与账号

一个 channel 插件代表一个平台。每个账号仍是独立 channel 实例，拥有自己的精确 ID、
凭据、active 状态和持久 Agent 绑定。保存一个账号不能覆盖另一个账号的凭据，也不能
重新启用管理员已禁用的平台。

Listener 检查 Native 全局开关、逐 Agent 禁用和实例 active 状态。某个用户的工具限制
不会停止其他用户共享的 listener。事件准入还检查已有 Agent 访问权限或访客策略。
渠道签名、开户和平台身份校验保留在所属 adapter。

现有 `UNIQUE(agent_id, type)` 唯一约束仍规定一个 Agent 每个平台最多绑定一个实例。
每个实例有自己独立的凭据，即使平台相同也不会互相覆盖。多个账号可以绑定不同 Agent。
身份关联与创建 bot 账号是独立操作。

## MCP 凭据与观测

MCP 声明是文件，可以是 `mcp/<name>.json` 独立文件，也可以属于完整包。声明保存
端点、传输方式、公开请求头、认证类型、凭据模式和不透明的凭据引用。秘密值不会写入
文件。OAuth grant、刷新令牌、客户端密钥和连接 generation 保存在独立授权存储中，
通过文件资源的规范身份和凭据所有者寻址。

发现过程读取当前 turn 捕获的声明。工具目录和状态只是当前目标与所有者的观测，不是
权威 registration 表。目录缺失或过期时，后续 turn 可以再次发现；未变化的会话可以
复用连接。Skill、描述或其他非认证文件编辑不会改变 grant；只有端点、授权服务器、client
或其他认证身份变化才会生成新目标，不能复用旧 grant。

删除文件会移除声明和后续选择，但不会撤销 grant。Disconnect 是明确的本地撤权动作：它
撤销匹配的已存 grant、轮换 generation、关闭匹配连接，并拒绝迟到回调或刷新；不承诺远端
provider 一定撤权。共享 system-agent 资源使用共享 owner tuple，per-user 资源使用已验证
的用户 tuple。所有者不会从请求 payload 推断，迁移也不会伪造所有者。

## Core 边界与升级

Provider adapter 与沙箱后端保留显式的编译期 registry。Core 存储、编排和凭据服务
不会因为被插件使用就变成可选能力。例如，禁用公开 Xberg 插件会隐藏其公开资源，
Library 通过显式路径调用的内部解析器依赖仍可用。

`cmd/stellad` 组合各后端和公共 catalog。后端不能另建 scope 或 enabled 解析规则。
Provider 与沙箱 adapter 的生产代码依赖 `pkg/**` 公开契约，不依赖 `internal/**`。

旧数据切换需要维护升级：先停止所有旧写入进程，再启动新运行态。迁移把完整文件发布到
四个类型化资源根，校验凭据关联和工具策略，并记录来源与目标摘要。历史行保留供业务
证据读取，但运行时发现不依赖它们。Native 全局配置继续使用现有存储。这次切换不支持
新旧进程对同一数据库滚动写入。
