---
title: 技能
---

## 什么是技能

技能是可复用的操作手册，教会 Stella 如何执行特定任务。当你让 Stella 做一些事情，比如"创建一个 GitHub release"或"写一篇博客文章"时，她可以加载一个技能，获得该工作流程的分步指令。

技能用纯 Markdown 编写——本质上就是 Stella 阅读并遵循的速查表。你可以从公共注册中心安装技能，也可以自己编写。

## 技能作用域与优先级

Stella 当前有两类 Skill 权威。随发行版提供的 builtin 只来自不可变、内容寻址的发行 bundle。Project Skill 是持久 Agent/项目工作树中的普通文件。全局、Agent 绑定、用户和用户-Agent Skill 仍存储在 PostgreSQL 中，执行镜像从它们派生。后续将权威切换到 Home 文件系统的工作尚未落地。

存储的作用域为 `project`、`user_agent`、`user`、`system_agent` 和 `system`。`builtin` 是上下文作用域：发行版 Skill 使用不可变身份 `builtin:<name>`。管理员安装的全局 Skill 是另一个可变身份 `system:<name>`，绑定到 Agent 的管理员 Skill 则是 `system_agent:<name>`。

- **项目技能** — 存放在你的仓库的 `.agents/skills/` 目录下。它们随代码发布，并在当前会话绑定到该项目时可用。
- **用户技能** — 你的个人技能，对你的所有代理可用。
- **用户 · 当前代理** — 你限定于单个代理的个人技能。
- **共享代理技能** — 由管理员管理，对使用该代理的所有人可用。
- **全局技能** — 由管理员管理，处处可用。Stella 随附的技能仍属于安装内容；管理员可以在管理控制台安装、启用、停用和删除受管理的全局技能。

同名时，Stella 按以下顺序选择唯一的胜出项：

```
项目 > 用户 · 当前代理 > 用户 > 共享代理 > 全局 > builtin
```

策略在选择胜出项后才应用。禁用胜出项不会让同名的低优先级 Skill 出现。

## 按 Agent 启用

Skill 对 Agent 默认启用。管理员或该 Agent 的持久创建者可以在该 Agent 的 **技能** 标签页启用或禁用 builtin、全局或与该 Agent 匹配的 Agent Skill。这里编辑的是同一份共享设置，最后一次成功提交的更新生效。

启用状态与编辑 Skill 内容的权限、以及 `disable_model_invocation` 相互独立。已被接纳的 turn 保留自己的 Skill 快照；下一次 turn 才会看到已提交的启用状态变更。

旧版非空启用数组会显示为诊断信息，但其含义是所有 Skill 均启用。指向已不存在 Skill 的禁用引用不影响执行；请在 Web UI 中显式清除。

降级 Stella 前，请重新启用每个已禁用的 Skill，并清除所有悬空的禁用引用。旧版二进制可能忽略 AgentSkillPolicy v1，并在普通 Agent 编辑时覆盖它。请勿将混合版本部署中的 Skill 启用状态视为安全保证；它是产品偏好设置，而不是文件系统访问控制。

在 **个人设置 → 技能** 管理个人的 `user` 与 `user_agent` 技能。管理员在 **管理控制台 → 部署资源 → 全局技能** 管理部署所有的 `system` 与 `system_agent` 技能。两个页面不会混合不同所有权的作用域。

## 安装技能

### 从对话中安装

让 Stella 搜索并安装技能：

- **"搜索一个关于 git release 的技能。"**
- **"安装 git-helper 技能。"**
- **"找一些跟代码审查相关的技能。"**

Stella 会搜索各个注册中心，展示可用的结果。然后你可以选择要安装哪个。

### 从注册中心安装

Stella 可以从多个来源安装技能：

- **[clawhub.ai](https://clawhub.ai)** — 主要的技能注册中心。可以直接在对话中搜索和安装。
- **[skills.sh](https://skills.sh)** — 次要注册中心。搜索结果会与 clawhub.ai 合并。
- **GitHub / GitLab** — 安装托管在 Git 仓库中的任何技能。
- **本地路径** — 从文件系统中的目录安装。

如果你在 clawhub.ai 遇到频率限制，可以设置一个免费的 API 令牌：

1. 在 [clawhub.ai](https://clawhub.ai) 注册。
2. 进入 Settings，然后 API Tokens，创建一个令牌。
3. 在聊天中发送：`/config CLAWHUB_TOKEN your-token`

## 管理技能

### 从对话中管理

- **"列出我安装的技能。"**
- **"移除 git-helper 技能。"**
- **"加载部署技能。"** — Stella 读取该技能的指令用于当前任务。

### 从 Web UI 管理

在个人设置中浏览、安装和移除你的技能。管理员通过“全局技能”管理部署级与共享代理技能。

## 创建自定义技能

你可以创建自定义技能来教 Stella 你的工作流程。一个技能就是一个包含 `SKILL.md` 文件的目录。

### 技能格式

```markdown
---
name: my-deploy-script
description: Deploy the application to production.
---

# Deploy to Production

Follow these steps to deploy:

1. Run the test suite and confirm all tests pass.
2. Build the production bundle.
3. Push to the production branch.
4. Verify the deployment is healthy.

Always ask the user for confirmation before pushing to production.
```

### Frontmatter 字段

| 字段          | 必填 | 描述                                    |
| ------------- | ---- | --------------------------------------- |
| `name`        | 是   | 小写加连字符，最长64个字符              |
| `description` | 是   | 一行摘要，显示在搜索结果中              |
| `status`      | 否   | `draft`、`active`（默认）、`deprecated` |

### 保存自定义技能

你可以在对话中创建技能：

- **"创建一个叫 'deploy' 的技能，描述我们的部署流程。"**

Stella 通过内置的 skills 工具直接创建和管理技能，无需 CLI。

## 小贴士

- **先搜索再创建。** 在从头创建技能之前，先检查注册中心里是否已经有了。
- **保持技能专注。** 一个技能对应一个任务。一个"部署"技能和一个"回滚"技能比一个试图同时做两件事的技能要好。
- **团队工作流程使用项目技能。** 把共享技能放在仓库的 `.agents/skills/` 目录中，让团队所有人受益。
- **通过加载来测试技能。** 创建技能后，让 Stella 加载它并尝试工作流程，验证指令是否有效。
