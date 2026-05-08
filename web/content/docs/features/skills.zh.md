---
title: Skills（技能）
---

## 概述

Skills（技能）是可复用的操作手册——以 Markdown 文件的形式告诉 Agent 如何完成某项任务。对话中可按需加载，支持从外部注册表安装或在本地创建。

Anna 支持三种技能作用域：

| 作用域      | 位置                             | 可写权限    |
| ----------- | -------------------------------- | ----------- |
| **project** | `{PROJECT_ROOT}/.agents/skills/` | Git（只读） |
| **user**    | 数据库，按用户加密存储           | 用户        |
| **agent**   | 数据库，绑定到特定 Agent         | 用户        |

Project 技能随代码仓库一起分发，优先级高于同名的 user/agent 技能。

## 注册表

### clawhub.ai

[clawhub.ai](https://clawhub.ai) 是主要技能注册表，可在对话中直接搜索和安装：

```
skills action=search query=<关键词>
skills action=install source="clawhub:<slug>"
skills action=install source="clawhub:<slug>@<版本>"
```

**速率限制。** 匿名访问的请求配额较低。遇到 429 错误时，请设置免费 API Token：

1. 在 [https://clawhub.ai](https://clawhub.ai) 注册或登录
2. 进入 **Settings → API Tokens** → 创建 Token → 复制
3. 在对话中发送：
   ```
   /config CLAWHUB_TOKEN <你的token>
   ```

Anna 在遇到 429 时会自动切换到国内镜像（`cn.clawhub-mirror.com`），大多数用户无需设置 Token。

**环境变量**

| 变量            | 说明                               |
| --------------- | ---------------------------------- |
| `CLAWHUB_TOKEN` | 认证访问的 Bearer Token            |
| `CLAWHUB_URL`   | 覆盖注册表地址（默认：clawhub.ai） |

### skills.sh

[skills.sh](https://skills.sh) 是备用注册表，每次 `search` 调用会与 clawhub.ai 结果合并返回。安装格式：

```
skills action=install source="owner/repo@skill-name"
```

### GitHub / GitLab

从 Git 仓库安装任意技能：

```
skills action=install source="owner/repo@skill-name"
skills action=install source="https://github.com/owner/repo/tree/main/path/to/skill"
```

### 本地路径

```
skills action=install source="/path/to/skill"
skills action=install source="./relative/path"
```

## 管理技能

| 操作 | 示例                                                 |
| ---- | ---------------------------------------------------- |
| 搜索 | `skills action=search query=git`                     |
| 安装 | `skills action=install source="clawhub:git-helper"`  |
| 列表 | `skills action=list`                                 |
| 加载 | `skills action=load name=git-helper`                 |
| 删除 | `skills action=remove name=git-helper`               |
| 创建 | `skills action=create name=my-skill description=...` |
| 更新 | `skills action=patch name=my-skill status=active`    |
| 弃用 | `skills action=deprecate name=my-skill`              |

在 install/remove/create 时加上 `scope=agent` 可将目标切换为当前 Agent 作用域（默认为 user）。

## 技能格式

一个技能是包含至少一个 `SKILL.md` 文件的目录，文件需带有 YAML frontmatter：

```markdown
---
name: my-skill
description: 在搜索结果中显示的单行描述。
status: active
---

# My Skill

此处填写给 Agent 的操作说明。
```

**Frontmatter 字段说明**

| 字段                       | 必填 | 说明                                             |
| -------------------------- | ---- | ------------------------------------------------ |
| `name`                     | 是   | 小写字母，仅允许连字符，最多 64 个字符           |
| `description`              | 是   | 显示在 `list` 和 `search` 输出中                 |
| `status`                   | 否   | `draft` / `active`（默认）/ `deprecated`         |
| `disable-model-invocation` | 否   | 为 `true` 时，技能注入系统提示但模型不可直接调用 |
