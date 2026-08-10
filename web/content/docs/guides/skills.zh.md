---
title: 技能
---

## 什么是技能

技能是可复用的操作手册，教会 Stella 如何执行特定任务。当你让 Stella 做一些事情，比如"创建一个 GitHub release"或"写一篇博客文章"时，她可以加载一个技能，获得该工作流程的分步指令。

技能用纯 Markdown 编写——本质上就是 Stella 阅读并遵循的速查表。你可以从公共注册中心安装技能，也可以自己编写。

## 技能作用域

技能按"由谁管理"和"在哪里生效"来组织：

- **项目技能** — 存放在你的仓库的 `.agents/skills/` 目录下。它们随代码发布，并在当前会话绑定到该项目时可用。
- **用户技能** — 你的个人技能，对你的所有代理可用。
- **用户 · 当前代理** — 你限定于单个代理的个人技能。
- **共享代理技能** — 由管理员管理，对使用该代理的所有人可用。
- **全局技能** — 由管理员管理，处处可用。Stella 随附的技能仍属于安装内容；管理员可以在管理控制台安装、启用、停用和删除受管理的全局技能。

同名时，越具体的作用域优先级越高。解析顺序（从高到低）：

```
项目 > 用户 · 当前代理 > 用户 > 共享代理 > 全局
```

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
