---
title: 技能
---

Skill 是教 Stella 执行某项任务的可复用操作手册。一个 Skill 是包含
`SKILL.md` 的目录，也可以带有参考文件或脚本。任务符合描述时，Stella
可以加载它。

## Skill 的来源

项目 Skill 位于当前项目的 `.agents/skills/`。个人和托管 Skill 可以从
**个人设置 > 技能** 安装，管理员还可以从 **管理控制台 > 部署资源 > 全局技能**
安装。Agent Plugin 提供的 Skill 跟随所属插件的启用状态，没有第二个 Skill
开关。随 Stella 发布的内置 Skill 属于发行版内容。

同名 Skill 同时可见时，Stella 按以下顺序选择：

```
项目 > 当前 Agent > 你的 Skill > 共享 Agent > 全局 > 内置
```

Stella 先选出胜者，再应用该 Skill 的策略。禁用胜者不会让同名的低优先级
Skill 重新出现。Stella 在捕获下一轮对话时读取项目文件，因此项目 Skill 的
修改在下一轮生效。已经开始的 turn 保留自己捕获的 Skill 视图。管理员设置的
**系统 · 全部 Agent** 或 **系统 · 当前 Agent** 停用是上限，更窄作用域的个人启用
不能解除它。

## 安装和配置

每次安装或上传前都要选择目标位置。Stella 不会从对话推断目标位置。

- 在 Agent 的 **技能** 页面中选择 **我的 · 当前 Agent**。管理员还可以选择
  **系统 · 当前 Agent**。
- 在 **个人设置 > 技能** 中选择 **我的 · 全部 Agent** 或 **我的 · 当前 Agent**。
- 在 **管理控制台 > 部署资源 > 全局技能** 中选择 **系统 · 全部 Agent** 或
  **系统 · 当前 Agent**。

Web UI 支持从远程来源安装，例如技能名称或 GitHub 仓库，也支持上传 ZIP。
上传的压缩包必须包含一个带有 `SKILL.md` 的 Skill 目录。插件 Skill 由管理员
在 Plugins 页面导入，并保留包声明的文件和资源名称。

安装插件后，在插件设置中配置它的作用域。配置可以启用或禁用插件、选择已声明
的 CLI 版本、设置 MCP 端点，并启动需要的 OAuth 授权流程。配置页面展示已声明的
资源，以及端点或 OAuth 客户端字段是否已填写。它不会运行命令、连接每一个 MCP
服务器，也不能证明下一轮一定能准备好全部资源。这个摘要不是某次执行的回执。
在该轮用户消息下展开**执行摘要**，可以查看该轮实际接纳的不可变包版本和 digest、配置
ID/作用域/修订、授权与就绪状态、Skill 胜者状态（`selected`、`masked` 或 `overridden`），
以及 CLI 请求版本、解析版本和安装证据。复用旧缓存或镜像预装产物时，解析版本或安装证据可能
未知。加入这项元数据前记录的 turn 没有回溯回执，Stella 不会根据当前配置重建它。

## 使用 Skill

先让 Stella 查找已安装的 Skill，再加载与任务匹配的那个。`skill_installed_search`
只搜索当前 Agent 已可见的 Skill，不搜索市场。`skill_load` 读取选中的 revision，
并把它复制到当前会话的临时 sandbox 目录。返回的路径是一次性的，Skill 的脚本和
附带文件应从这个路径读取。

每轮开始时，Stella 选择胜出的 Skill，并按同一套插件配置准备所选资源。资源可能
包括固定版本的 CLI、环境绑定、MCP 工具目录和所需账号权限。如果插件缺少必需授权
或 CLI 准备失败，该插件的资源不会进入本轮。Stella 会保留失败原因，也不会静默
改选同名的低优先级资源。

插件更新的 **预览** 会报告候选版本、内容 digest、资源名称、OAuth 变化，以及现有
配置不兼容的作用域。预览只读取并校验候选包，不会发布包、安装 CLI、连接 MCP
服务器或授予账号权限。真实执行仍可能因为账号授权缺失、sandbox 内命令安装失败
或远端服务不可达而失败。

## 更新、停用、撤权和卸载

更新托管 Skill 时，用完整目录或 ZIP 替换它，并使用最近一次读取返回的版本重试。
版本冲突表示其他人已经修改了 Skill，重新读取后再决定下一步。更新插件时先运行
**预览**，检查候选版本和不兼容作用域，再发布更新。发布成功后，新 turn 使用新的
插件版本。已经接纳的 turn 会一直使用之前的插件和 Skill 快照，直到它结束。

停用插件会阻止新 turn 使用该插件。普通插件或 Skill 配置变化允许已经接纳的 turn
完成。将 Skill 的 `disable-model-invocation` 设为 `true` 只会禁用该 Skill 的自动调用，
不会停用 Skill 本身，也不改变编辑权限。停用插件不会撤销已有 OAuth grant。

需要撤权时，请使用所属账号、Agent assignment、OAuth 或 Vault 的控制项。Stella 会
拒绝撤权作用域内的新调用，取消或 detach 匹配的活动工作，并关闭 runner。已经复制
到 sandbox 的文件和已经在外部服务产生的副作用无法收回。

卸载托管 Skill 会让它不再参与后续选择。退休自定义插件会停止新选择，并等待活动
turn 及其他仍合法的文件使用者结束后再删除包数据。因此数据库变更完成后，界面仍可能显示
内部清理状态为 `cleanup_pending`。无法证明进程及其后代已经停止时，Stella 会保留文件。`local`
和 `none` sandbox backend 会在这种情况下保留恢复 marker；marker 会阻止整个部署的
插件和托管 Skill 清理，而且永远不会自动移除，因此即使 turn 正常关闭，也可能无限期阻塞
清理。目前没有可以清除它的产品命令或安全自动恢复路径。没有 TTL 或猜测进程 ID 的逻辑
可以安全地提前清除它。

## 在对话中管理 Skill

当 Agent 启用了对话式 Settings 工具时，Stella 可以使用以下工具管理托管 Skill：

- `settings_skill_list` 和 `settings_skill_get` 读取安全元数据、文件名和当前版本，
  不会返回文件内容。
- `settings_skill_create` 从 sandbox 路径中的完整目录或 ZIP 创建 Skill。
- `settings_skill_update` 替换完整包，并要求使用 `settings_skill_get` 返回的版本。
- `settings_skill_delete` 删除 Skill，并要求使用同一个版本。

这些工具仍遵守调用者的所有权和 Agent 权限。远程来源安装、浏览器 ZIP 上传、插件
包导入和凭据绑定继续使用 Web UI 或 API。`skill_load` 是运行时读取操作，与托管
Skill 管理分开。

## 创建 Skill

```markdown
---
name: my-deploy-script
description: Deploy the application to production.
---

# Deploy to production

1. Run the test suite.
2. Build the production bundle.
3. Verify the deployment is healthy.
```

`name` 只能使用小写字母、数字和连字符，最长 64 个字符。`description` 必填，并会
显示在搜索结果中。`disable-model-invocation` 可选。需要只有明确请求时才自动调用 Skill
时，将它设为 `true`；它不会停用 Skill 本身。

团队工作流可以把 Project Skill 提交到 `.agents/skills/`。托管 Skill 则上传完整目录
或 ZIP 到指定作用域。

## 小贴士

创建前先搜索已有 Skill。一个 Skill 专注一个工作流。创建或更新后先加载它，运行一个
小型代表性任务，再把它用于正式工作。
