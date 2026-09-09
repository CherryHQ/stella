---
title: 技能
---

Skill 是教 Stella 执行某项任务的可复用操作手册。一个 Skill 是包含
`SKILL.md` 的目录，也可以带有参考文件或脚本。Stella 在每轮开始时读取当前
Agent 选中的文件。

## 作用域和优先级

Stella 读取四个有明确类型的资源根：

| 作用域         | 文件位置              | 适用范围             |
| -------------- | --------------------- | -------------------- |
| `system`       | 部署资源              | 所有用户和 Agent     |
| `system_agent` | 某个 Agent 的系统资源 | 该 Agent 的所有用户  |
| `user`         | 某个用户的资源        | 该用户的所有 Agent   |
| `user_agent`   | 某个用户的 Agent 资源 | 一个用户和一个 Agent |

项目中的 `.agents/skills/` 只对 Skill 生效，并优先于包内 Skill。对于每个包名，
Stella 只在四个资源根中按以下顺序选择最具体的完整包：

```
user_agent > user > system_agent > system
```

选中的包会整体替换更宽作用域中的同名完整包。包内的文件、Skill、CLI、环境绑定
和 MCP 声明作为一个整体解析。独立副本没有来源链接，不会自动接收来源后续更新。
独立 Skill 按名称遵循同样的作用域优先级。

`settings.json` 可以停用资源，也可以添加只有管理员能设置的 `forbidden` 和
`disabled_tools`。被停用或解析失败的胜者会遮蔽继承资源。更窄作用域的个人设置
不能解除管理员禁止项。

Stella 在接纳 turn 时捕获资源文件。turn 中途的编辑在下一轮生效，本轮继续使用
已经捕获的内容。搜索、提示词加载、CLI 准备和 MCP 发现也使用同一份捕获视图。

## 安装和编辑

每次安装或上传前都要选择目标作用域。Stella 不会从对话推断作用域。Web UI 和
API 可以在可写作用域创建、复制、编辑和删除完整包或独立 Skill。包编辑会替换
完整目录；API 提供摘要时，文件编辑必须带当前摘要，过期编辑会返回冲突。Shell
直接写文件是支持的，但不提供多文件事务或文件系统 compare-and-swap 保证。

上传的压缩包必须包含一个带有 `SKILL.md` 的 Skill 目录。复制包会在目标所有者下
创建新的独立资源，拥有自己的文件、设置、凭据和 OAuth grant；之后编辑来源不会
升级副本。

## 使用 Skill

先让 Stella 查找已安装的 Skill，再加载与任务匹配的那个。`skill_installed_search`
只搜索当前 Agent 可见的 Skill，不搜索市场。`skill_load` 把选中的文件读入当前会话的
临时 sandbox 目录。返回路径是一次性的，Skill 的脚本和附带文件应从该路径读取。

如果资源准备失败，Stella 会在本轮保留选中的包为 masked，不会静默恢复更低优先级的
同名资源。Native 工具属于独立的系统能力，Skill 或同名包不会因此获得 Native 权限。

## 更新、停用、断开和删除

更新 Skill 或包时，在目标作用域编辑或替换完整文件。摘要冲突后先重新读取
当前资源，再重试。成功写入会在下一轮生效，本轮继续使用已捕获的 Skill 和包视图。

停用包或 Skill 会阻止后续选择，不会撤销 OAuth grant，也不会删除资源字节。删除
MCP 声明文件只删除声明本身。需要撤销本地访问时，使用 MCP 的 **Disconnect**，它会
撤销匹配的已存 OAuth grant、关闭连接并阻止迟到的回调或刷新；不承诺远端 provider 一定
撤权。两者是独立动作。

卸载 Skill 或删除包会让它不再参与后续选择。Stella 保留 Skill 反射所需的历史 usage
和 changelog 证据，也不会在线清理资源字节或缓存。只有在能够证明相关进程及其后代
已经停止后，明确的维护操作才能清理这些数据。

local backend 会执行配置的沙箱策略，但不能证明脱离进程组的后代已经停止；`none` sandbox
不提供可靠的进程隔离。不要把正常 turn 关闭当作后代进程已停止的证明，也不要用 TTL
或猜测进程 ID 清除资源字节。需要严格进程边界的清理场景不适合使用 `none` backend。

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
显示在搜索结果中。`disable-model-invocation` 可选。需要只有明确请求时才自动调用
Skill 时，将它设为 `true`；它不会停用 Skill，也不会改变编辑权限。

团队工作流可以把 Project Skill 提交到 `.agents/skills/`。共享或个人 Skill 则应在
指定作用域创建或上传完整目录。

## 小贴士

创建前先搜索已有 Skill。一个 Skill 专注一个工作流。创建或更新后先加载它，运行一个
小型代表性任务，再把它用于正式工作。
