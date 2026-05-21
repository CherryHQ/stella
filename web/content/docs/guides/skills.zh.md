---
title: 技能
---

## 什么是技能

技能是可复用的操作手册，教会 Stella 如何执行特定任务。当你让 Stella 做一些事情，比如"创建一个 GitHub release"或"写一篇博客文章"时，她可以加载一个技能，获得该工作流程的分步指令。

技能用纯 Markdown 编写——本质上就是 Stella 阅读并遵循的速查表。你可以从公共注册中心安装技能，也可以自己编写。

## 技能作用域

技能分为三个作用域：

- **项目技能** — 存放在你的仓库的 `.agents/skills/` 目录下。它们随代码发布，并在当前会话绑定到该项目时可用。
- **用户技能** — 存储在你账户中、面向当前代理的个人技能。
- **代理技能** — 限定于特定代理。适用于不同代理需要不同工作流程的场景。

同名时，项目技能优先于用户技能和代理技能。

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

### 从 CLI 安装

```bash
# 搜索技能
stella skill search "git"

# 为某个代理从 clawhub.ai 安装
stella skill install --agent-id <agent-id> "clawhub:git-helper"

# 安装特定版本
stella skill install "clawhub:git-helper@1.2.0"

# 从 GitHub 安装
stella skill install "owner/repo@skill-name"

# 从本地路径安装
stella skill install "/path/to/skill"
```

## 管理技能

### 从对话中管理

- **"列出我安装的技能。"**
- **"移除 git-helper 技能。"**
- **"加载部署技能。"** — Stella 读取该技能的指令用于当前任务。

### 从 CLI 管理

```bash
# 列出某个代理可见的技能
stella skill list --agent-id <agent-id>

# 包含项目会话中的项目技能
stella skill list --agent-id <agent-id> --session-id <session-id>

# 从代理下移除一个用户技能
stella skill remove --agent-id <agent-id> "git-helper"
```

要为特定代理（而非你的用户账户）安装技能，在对话中指定 `scope=agent`，或在 CLI 中使用相应的标志。

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

或者通过 CLI：

```bash
stella skill create --name "my-skill" --description "What this skill does"
```

技能正文是 frontmatter 之后的 Markdown 内容。编写清晰的分步指令——Stella 会按字面意思执行。

## 小贴士

- **先搜索再创建。** 在从头创建技能之前，先检查注册中心里是否已经有了。
- **保持技能专注。** 一个技能对应一个任务。一个"部署"技能和一个"回滚"技能比一个试图同时做两件事的技能要好。
- **团队工作流程使用项目技能。** 把共享技能放在仓库的 `.agents/skills/` 目录中，让团队所有人受益。
- **通过加载来测试技能。** 创建技能后，让 Stella 加载它并尝试工作流程，验证指令是否有效。
