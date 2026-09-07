---
title: 插件系统
---

Agent Plugins 通过一份带范围的包定义组合 Skills、CLI 依赖、环境绑定和 MCP 服务。
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
Native 管理 API 只接受管理员认证，OAuth access token 无法访问。

## 定义与配置

`PluginDefinition.ID` 是唯一的规范包名，定义还包含发行资源与默认启用状态。
Builtin 定义来自可信的发行声明，数据库中的 builtin 行只是投影。
同一 Agent 定义可以同时包含多种资源。传输和安装方式由各自的资源消费者选择，
定义不再具有根级后端分类。编译进程序的 Go 实现通过独立 Native 路径注册和管理。

管理路由使用 `/api/plugins/{plugin_id}` 及其子资源。将目录返回的完整 ID 编码为一个
URL 路径段；源码目录分类不参与请求寻址。
名称由 1–64 个小写字母、数字、点和短横线组成，首尾必须是字母或数字，不能包含
`..` 或 `--`。重名创建失败，不自动添加后缀。创建后身份不可修改，显示名称可以修改。

`PluginConfig` 保存某个定义在一个范围内的决策：

| 范围         | 适用对象               |
| ------------ | ---------------------- |
| System       | 部署中的所有用户       |
| System agent | 某个 Agent 的所有用户  |
| User         | 某个用户的所有 Agent   |
| User agent   | 某个用户使用某个 Agent |

每个定义在一个范围元组内至多一份配置。选择顺序是 user agent、user、system
agent、system。System 或匹配的 system agent 显式设为 `false`，分别构成独立上限，
更窄范围的 `true` 不能解除其中任何一个限制。`null` 使用所选定义的发行默认值。

Agent Plugin 的配置模型是一份 `PluginDefinition`，加上四种范围元组各自至多一份
`PluginConfig`。`user_id` 和 `agent_id` 由可信 authority 推导，不能接受调用方自填身份。
Definition 拥有稳定的包身份和资源声明；所选 Config 拥有该范围的资源 payload 与凭据引用。

所选范围独立拥有配置，可以覆盖发行定义中的字段，但不同范围之间不合并字段或凭据。
所选配置禁用或不完整时，不回退到更宽范围。Builtin 使用相同规则，管理员可以禁用。

## 一份执行快照

公共服务从可信用户、Agent 或群组身份解析快照。Agent 运行时从同一份快照一起生成
资源可见性、二进制、环境绑定与声明式 Prompt。上下文构造函数只接收快照，调用方
不能另行传入其他身份或版本的资源。Native Host 只通过独立策略管理 Go 注册的能力
与原生 Prompt。

每个 Agent Plugin 按精确包 ID 解析，不同包不会替换彼此的资源。Native 工具和 hooks
不进入这份快照；同名 Agent 包不能获得 Native 准入，Native 仍使用可信注册 ID 和独立策略。

MCP 导出名由包名、server key 和远端工具名适配为最多 64 字符的 ASCII 名称，
带确定性的 12 位十六进制哈希后缀。Server key 使用包中声明的 MCP 条目名，
导入的旧单服务器使用 `main`。实际暴露前检查整组工具是否重名。
授权使用包身份、server key 和远端原始工具名，不解析展示名称。
Native 工具保留已注册的静态名称。

配置写入在执行准入屏障内原子提交。空闲 runner 被回收，已经开始的 turn 可以结束后
再回收 runner。凭据读取仍必须匹配捕获的配置版本，旧 runner 不能把新凭据发给旧地址。
插件开关约束 Stella 管理的能力暴露与执行准入；文件系统和网络限制仍由沙箱负责。
禁用插件不会撤销已有 OAuth grant，也不会抹除此前已加载的 Skill。

## CLI 与 Skill 资源

CLI 集成可以包含二进制、Skills、环境声明和提示。CLI 版本与 Skill 来源是独立字段，
更新一个不要求更新另一个。Manifest 只是发行输入的加载器，不再拥有独立权限规则。
只有 mise 和 Xberg 是同步准备的内嵌发行运行时。其他 CLI，包括 fd 和 rg，
都在后台通过会话选择共用的安装器预装。预热使用临时私有配置填充 mise artifact 缓存，
不发布会话选择，也不维护第二份安装状态文件。Runner 从匹配缓存准备选中的 snapshot，
缺失版本在对应沙箱边界内安装。

Builtin Skill 的归属由发行包声明生成，用户 frontmatter 不能认领 owner。
提示列表、搜索与直接加载都在选定资源后检查同一归属限制。

email、recally、scheduler 指南是位于 `plugins/agent/<name>/` 的标准 Agent 包，
以 `plugin.json` 和 `skills/<name>/SKILL.md` 为编写来源，Agent Plugin 身份使用包的裸名。
禁用指南只隐藏它的 Skill，不改变对应 Native 能力。加载指南不会启用 Native 工具；
指南的 compatibility 说明会指出，对应 Native 能力需要单独可用。

所有内置 Agent 包使用 `plugins/agent/<name>/plugin.json`。Web 拥有自己的 Skill，
并声明 Bun 与 Lightpanda；禁用独立的 Bun 包不会禁用 Web。Python Script 归 uv 所有。
禁用包会隐藏该包控制的资源，但不删除共享的二进制缓存。Lightpanda 提供 Web 渲染，
Bun 运行抓取与搜索脚本。

每个 runner 只获得选中的 CLI 文件及入口。可信 system 安装的私有参数不进入 runner
可读的文件系统；Docker 在现有工具缓存内按一个解析后的 image ID 和完整四层范围选择
准备 Linux 文件。selection helper 只提供选中的条目和二进制。Native managed 安装使用
managed tree；User 和 user agent 的安装在各自沙箱目录内执行，并在 PATH 中优先。插件权限控制
Stella 提供的资源；`none` 后端不提供文件系统隔离。

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

每个 MCP 服务都是 Agent Plugin 内的一项资源。所选父配置用 `mcp_servers`
按声明的服务名保存端点设置，凭据引用使用相同的 key。每个子服务在父配置下具有稳定
UUID，`(config_id, server_key)` 唯一。子关系只保存身份，不复制第二份端点或认证配置。
Token 保留在 Vault，shared 与 per-user 凭据互不回退。

凭据、OAuth flow 和连接观测使用子服务 UUID；范围、调用者授权与版本屏障属于父配置。
子服务写入必须验证归属，并只消费一次父版本。修改端点或认证身份只清理受影响子服务
的状态；移动或删除父配置时，全部子服务在一个事务内处理。一个子服务失败不会隐藏
正常的兄弟服务或包内 Skill。

OAuth 客户端注册属于各个子服务，由父配置所有者管理。System 和 system agent 配置缺少客户端时，
管理员先通过 OAuth start 初始化，随后用户授权自己的账号。User 和 user agent
配置的所有者可以自行初始化。旧系统级配置若没有 client ID，升级后需要这一次管理员
操作；各用户的 token 仍独立保存。禁用和 reset 保留 grant，删除配置才原子清理其 grant。

远端工具目录和连接状态以子服务 UUID 及凭据所有者为键，并检查父配置版本。
某个用户的工具目录不能成为另一个用户的工具列表。旧 per-user 目录没有可信 owner
来源，迁移后必须冷探测。内部 OAuth bundle 不允许通过公开 Vault 接口访问，也不进入
通用环境变量。

## Core 边界与升级

Provider adapter 与沙箱后端保留显式的编译期 registry。Core 存储、编排和凭据服务
不会因为被插件使用就变成可选能力。例如，禁用公开 Xberg 插件会隐藏其公开资源，
Library 通过显式路径调用的内部解析器依赖仍可用。

`cmd/stellad` 组合各后端和公共 catalog。后端不能另建 scope 或 enabled 解析规则。
Provider 与沙箱 adapter 的生产代码依赖 `pkg/**` 公开契约，不依赖 `internal/**`。

旧数据切换需要维护升级：先停止所有旧写入进程，再启动新运行态。一个事务导入并验证
配置、凭据关联与工具策略，最后记录完成。导入后的旧 Agent Plugin 行保留供检查，
Native 全局配置继续使用现有存储。这次切换不支持新旧进程对同一数据库滚动写入。
