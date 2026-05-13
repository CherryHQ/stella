---
title: Recally - 阅读助手
---

## 概述

Recally 是 Stella 的阅读助手 —— 一个用于保存、组织和回顾网页内容的系统。它可以让你建立一个个人内容库，保存文章、论文、推文、视频以及任何其他在对话中遇到的基于 URL 的内容。

与简单的书签不同，Recally 将完整的文章内容存储为带有结构化元数据的 Markdown 文件（标题、摘要、标签、作者、来源类型），为所有内容建立索引以便快速搜索，并与 Stella 的 Agent 系统集成，让你日后可以向 Agent 询问已保存内容的相关问题。

## 核心功能

- **通用 URL 支持**：网页文章、Twitter/X 帖子、YouTube 视频、GitHub 仓库、PDF、RSS 订阅
- **Agent 驱动的摘要**：Agent 针对每种来源类型使用最佳工具获取和总结内容
- **快速元数据搜索**：按标题、摘要、标签和作者搜索（FTS5 计划在未来支持）
- **RSS 订阅**：订阅源并通过调度器自动获取内容
- **每日摘要**：早晨推送已保存文章摘要、未读计数和值得回顾的内容
- **基于文件的存储**：完整内容存储在 Markdown 文件中；数据库仅保存轻量级索引

## 架构

recally CLI 是一个轻量的 REST 客户端。运行中的 stella server（`stella serve`）
是唯一直接读写 recally 数据库和磁盘 Markdown 文件的进程；CLI、Web UI 以及 SDK
消费者都走 HTTP。

```
用户发送 URL → Agent 加载 recally 技能
    |
    | tap/gh/kreuzberg 获取内容
    v
Agent 总结 + 提取元数据
    |
    | stella recally save --url ... --title ... --summary ...
    v
CLI 构造 JSON 请求 → POST /api/recally/articles  (Authorization: Bearer STELLA_TOKEN)
    |
    v
stella server: 写入 Markdown 文件 + 数据库索引行
    |
    v
库目录: $STELLA_HOME/library/{userID}/articles/{year}/{month}/{day}-{slug}.md
```

### 前置条件

执行任何 `stella recally …` 命令之前：

1. stella server 必须在本机或可经 HTTP 访问的远端启动（`stella serve`）。
2. 设置 `STELLA_TOKEN` 为账号生成的 token（server 需开启 `STELLA_VAULT_KEY` 才能启用 token 认证）。
3. 可选：设置 `STELLA_SERVER_URL` 指向远端 server，默认 `http://127.0.0.1:25678`。

CLI 不再直接打开 SQLite 数据库——这个职责完全在 server。

### REST API

完整契约见 [`api/recally.openapi.yaml`](https://github.com/CherryHQ/stella/blob/main/api/recally.openapi.yaml)。
资源：

- `GET/POST /api/recally/articles` —— 列表/搜索/upsert
- `GET/PUT/DELETE /api/recally/articles/{id}` —— 读/改/删；GET 加 `?include=content`
  返回 Markdown 正文
- `GET/POST /api/recally/feeds`、`GET/PUT/DELETE /api/recally/feeds/{id}`
- `POST /api/recally/feeds/{id}/poll` —— server 端拉 RSS，创建 pending entry
- `GET /api/recally/feeds/{feedId}/entries`、`PUT .../entries/{id}`
- `GET /api/recally/digest`

修改 spec 后运行 `mise run generate:api` 重新生成 server interface 和 client。

### 存储结构

文章存储在独立于 Agent 的用户级库中：

```
$STELLA_HOME/
├── library/
│   └── {userID}/
│       └── articles/
│           ├── 2026/
│           │   ├── 04/
│           │   │   ├── 29-go-concurrency-patterns.md
│           │   │   └── 30-rust-memory-safety.md
│           │   └── 05/
│           │       └── 01-llm-context-window-research.md
│           └── 2025/
│               └── 12/
│                   └── 25-year-in-review.md
```

数据库仅保存包含 URL、标题、摘要、标签、状态和相对文件路径的索引行。

### 来源类型

| 类型      | 获取策略                 | 工具                       |
| --------- | ------------------------ | -------------------------- |
| `web`     | 可读的 Markdown 提取     | `tap fetch`                |
| `twitter` | 推文文本 + 媒体          | `tap fetch`                |
| `youtube` | 元数据 + 字幕            | `tap fetch`                |
| `github`  | 仓库信息、议题、PR       | `gh` + `tap fetch`         |
| `pdf`     | 文本提取                 | `kreuzberg extract`        |
| `rss`     | 订阅源轮询 → 条目 → 保存 | `stella recally feed poll` |

## CLI 参考

### 文章命令

#### 保存文章

```bash
stella recally save --url <url> \
  --title "文章标题" \
  --summary "简要摘要" \
  --tags "go,concurrency" \
  --source-type web \
  --author "作者姓名"
```

- `--url` (必需): 原始文章 URL
- `--canonical-url`: 可选的标准 URL 覆盖，用于去重
- `--title`: 文章标题
- `--summary`: 简要摘要
- `--tags`: 逗号分隔的标签（可多次使用）
- `--source-type`: `web`、`twitter`、`youtube`、`github`、`rss`、`pdf`
- `--author`: 文章作者
- `--content-file`: 包含内容的文件路径（否则使用标准输入）
- `--metadata`: JSON 元数据字符串
- `--published-at`: 原始发布日期（RFC3339）

输出：包含 `id`、`file_path` 和 `created`（如果文章已更新则为 false）的 JSON

#### 列出文章

```bash
stella recally list [--status unread] [--starred] [--json]
```

筛选条件：

- `--status`: `unread`（未读）、`read`（已读）、`archived`（已归档）
- `--source-type`: 按来源类型筛选
- `--starred`: 仅显示标星文章
- `--limit`: 最大结果数（默认：50）
- `--json`: 以 JSON 格式输出

#### 搜索文章

```bash
stella recally search "并发模式" [--limit 20] [--json]
```

搜索标题、摘要、标签和作者，使用 LIKE 匹配（FTS5 计划在将来支持）。

#### 阅读文章

```bash
stella recally read <article-id>
```

输出完整的 Markdown 内容到标准输出。

#### 更新文章

```bash
stella recally update <article-id> --status read --starred
```

更新元数据。如果文件存在，还会重写文件的前言（frontmatter）。

- `--status`: `unread`（未读）、`read`（已读）、`archived`（已归档）
- `--starred`: true/false
- `--summary`: 新摘要
- `--tags`: 新标签（替换现有标签）

#### 删除文章

```bash
stella recally delete <article-id>
```

从数据库中删除并删除文件。

### RSS 订阅命令

#### 添加订阅

```bash
stella recally feed add <feed-url>
```

获取订阅源元数据并订阅。将现有条目创建为 `pending`（待处理）状态。

#### 列出订阅

```bash
stella recally feed list [--json]
```

显示已订阅的源及其最后检查时间和检查间隔。

#### 删除订阅

```bash
stella recally feed remove <feed-id>
```

取消订阅并删除所有条目。

#### 轮询订阅

```bash
stella recally feed poll [<feed-id>] [--limit 20] [--json]
```

轮询订阅源以获取新条目。如果不指定 `feed-id`，则轮询所有启用的源。返回待处理和可重试的条目（状态为 `pending` 或 `error` 且尝试次数 < 3）。

输出包括 `feed_id`、`new_entries` 计数和 `pending` 条目数组。

#### 标记条目

```bash
stella recally feed mark <feed-id> <entry-id> --status saved --article-id <article-id>
stella recally feed mark <feed-id> <entry-id> --status skipped
stella recally feed mark <feed-id> <entry-id> --status error --error "获取超时"
```

更新条目状态。自动递增 `attempts` 并设置 `processed_at`。

- `--status`: `saved`（已保存）、`skipped`（已跳过）、`error`（错误）
- `--article-id`: 当状态为 `saved` 时必需
- `--error`: 当状态为 `error` 时的错误消息

### 摘要命令

```bash
stella recally digest [--json]
```

输出结构化的 JSON 摘要：

- `yesterday`: 昨天保存的文章
- `counts`: 未读/已读/已归档/已标星计数
- `revisit`: 值得回顾的文章（未读超过 3 天）
- `top_tags`: 本周热门标签

## 认证

CLI 的所有操作都通过 `STELLA_TOKEN` 认证。Agent 沙盒会话会自动获得该 token，Recally 会从认证后的 token 解析用户。

在 Stella 外部直接使用 CLI 时，请在环境变量中提供有效的 `STELLA_TOKEN`。

## 技能使用

`recally` 系统技能（`internal/resources/skills/system/recally/SKILL.md`）教 Agent 如何使用 CLI 命令。用户无需记住 CLI —— 只需说：

- "保存这篇文章 [URL]"
- "帮我总结这个链接"
- "我读过关于 Go 并发的什么内容？"
- "检查我的 RSS 订阅"
- "给我今天的阅读摘要"

该技能包括：

- URL 分类（检测 Twitter、YouTube、GitHub、PDF）
- 按来源的获取策略（tap、gh、kreuzberg）
- 摘要格式模板
- RSS 工作流程（轮询 → 迭代 → 保存 → 标记）
- 摘要格式化说明

## RSS 调度

Recally 与 Stella 的调度器集成以实现自动 RSS 轮询。当用户首次订阅源时，技能会指导 Agent 创建定时任务：

```
调度器操作：add
名称：recally-rss
计划：every 1h
会话模式：reuse
消息：加载 recally 技能。运行 stella recally feed poll 检查新的 RSS 条目，然后按照 recally 技能 RSS 工作流程处理每个待处理条目。
```

对于每日摘要，Agent 可以创建：

```
调度器操作：add
名称：recally-digest
计划：cron 0 8 * * *
会话模式：reuse
消息：加载 recally 技能。运行 stella recally digest 并按照 recally 技能摘要格式为用户编写友好的每日阅读摘要。
```

## 文章生命周期

```
已保存 → 未读 → 已读 → 已归档
   ↓
已标星（与状态正交）
```

- **未读（Unread）**：保存时的默认状态
- **已读（Read）**：用户已阅读文章
- **已归档（Archived）**：已完成并归档
- **已标星（Starred）**：标记以便快速访问（与状态分离）

## 重复处理

文章按标准 URL 去重。CLI：

1. 从原始 URL 计算确定性的标准 URL（小写主机、删除跟踪参数、排序查询参数、删除片段）
2. 在数据库中检查现有的 `(user_id, canonical_url)`
3. 如果找到：更新元数据（标题、摘要、标签）并返回 `created: false`
4. 如果未找到：创建新文章，`created: true`

如果 Agent 在获取过程中发现了更好的标准 URL（例如来自 `<link rel="canonical">`），可以传递 `--canonical-url`。

## 检索层次结构

Agent 通过两步流程访问保存的内容：

1. **搜索元数据** (`stella recally search`)：通过标题、摘要、标签、作者进行快速的 LIKE 搜索。返回轻量级索引行。

2. **阅读完整内容** (`stella recally read`)：一旦 Agent 识别出相关文章，就会读取完整的 Markdown 文件来回答具体问题。

这保持了搜索的快速性，同时保留完整内容用于深度查询。

## 实现细节

| 组件             | 位置                                                   |
| ---------------- | ------------------------------------------------------ |
| CLI 命令         | `cmd/stella/recally.go`                                |
| 存储层           | `internal/recally/store.go`                            |
| 文件管理器       | `internal/recally/files.go`                            |
| URL 规范化       | `internal/recally/urlnorm.go`                          |
| 类型定义         | `internal/recally/types.go`                            |
| 技能文件         | `internal/resources/skills/system/recally/SKILL.md`    |
| 数据库架构       | `internal/db/schemas/tables/articles.sql`              |
| 数据库架构 (RSS) | `internal/db/schemas/tables/rss_feeds.sql`             |
| 数据库查询       | `internal/db/queries/articles.sql`                     |
| 数据库查询 (RSS) | `internal/db/queries/rss_feeds.sql`                    |
| 沙盒认证环境     | `internal/agent/sandbox/env.go`（注入 `STELLA_TOKEN`） |

## 未来改进

- **FTS5 全文搜索**：索引文章内容以实现更深入的搜索
- **语义搜索**：向量嵌入实现基于概念的检索
- **文章内容提取**：更好地处理付费墙内容
- **阅读进度**：跟踪用户在长文章中的阅读进度
- **文章间链接**：通过共享标签/主题自动检测相关文章
