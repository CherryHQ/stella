---
title: Agent 模板与内置资源
---

## 概述

Anna 自带一套**内置资源目录**，让全新安装即开即用，无需手动编写每一段提示。资源存放于 `plugins/tools/builtin/`，在构建期间被嵌入二进制。

共有四种内置资源：

| 类型 | 作用 | 运行位置 |
|------|------|----------|
| **Skill（技能）** | 可按需加载的知识或操作手册 | 启动时同步进 `skills(scope='system')` |
| **Soul（灵魂）** | 融入系统提示的个性/语气片段 | 在创建 agent 时被复制进去 |
| **Sub-agent（子代理）** | 受限工具集的任务工作者，供 `agent` 工具委派 | 启动时释放到 `$ANNA_HOME/agents/` |
| **Template（模板）** | 完整的 agent 初始化方案（模型 + 提示 + soul + 启用的技能） | 创建时读取一次，之后不再保留关联 |

## 模板（Templates）

模板是新建 agent 的完整起点。在管理面板点击 **Add agent**，你会看到模板网格和一张 "Start from blank" 卡。选择模板会预填：

- **模型** — 模板推荐的 provider/model 组合
- **系统提示** — 复制自模板引用的 soul
- **已启用的内置技能** — 以芯片形式显示在表单上，保存前可自由切换

用户手动输入始终优先。所有字段在保存前都可以编辑；保存之后 agent 与模板没有任何持久关联 — 更新模板不会影响已有 agent。

### 当前附带的模板

- `default` — 平衡型助理人格
- `coder` — 偏实现；启用 `code-review` 与 `implementation`
- `researcher` — 调研类工作流；启用 `research` 与 `task-planning`
- `writer` — 长文写作；启用 `docs-writing`

## Soul

Soul 是可复用的人格片段。它们不单独存于数据库 — 选择后其内容被复制进新 agent 的 `system_prompt`，之后文本归属于该 agent，可以自由演化。

附带的 soul：`default`、`coder`、`researcher`、`direct`、`teacher`。

`default` soul 替代了此前硬编码的 `template/soul.md`，`runner.DefaultAgentSoul()` 现在运行时从注册表解析获取。

## Sub-agent

Sub-agent 预设定义了 `agent` 委派工具使用的受限工作者（调研、代码评审、写作、编码）。启动时 runner 会把它们释放到 `$ANNA_HOME/agents/`，项目内的 `.agents/agents/` 可以通过同名文件覆盖。

附带的 sub-agent：`coder`、`researcher`、`reviewer`、`writer`。

## Skill 与按 agent 开关

每个 `scope='system'` 的技能天生对所有 agent 可见，因此朴素地增长内置技能目录会把所有技能同时塞进每个 agent 的提示 — 提示体积会迅速膨胀。

解决方式：`settings_agents.enabled_builtin_skills`（JSON 字符串数组）。agent 看到的技能目录为：

```
{常驻内置：anna}
 ∪ {enabled_builtin_skills 中列出的}
 ∪ {agent 范围的数据库技能}
 ∪ {user 范围的数据库技能}
```

`anna`（自我知识技能）永远启用。其他内置技能必须显式开启 — 通过你选择的模板（模板会替你设好）或手动切换表单上的芯片。

附带的技能：`anna`、`code-review`、`docs-writing`、`implementation`、`research`、`task-planning`。

## 新增一个内置资源

目录是增量式的 — 在对应子目录放入文件后即自动出现在各处：

```
plugins/tools/builtin/
├── skills/<id>/SKILL.md        # 技能（目录 + SKILL.md + 引用文件）
├── souls/<id>.md               # soul
├── subagents/<id>.md           # sub-agent 预设
└── templates/<id>.md           # 模板
```

每个文件至少需要 YAML frontmatter：`id`、`name`、`description`。模板通过 `soul_id` 引用 soul、通过 `skills: [names]` 数组指定启用的技能。

下次构建后，管理面板会通过 `GET /api/builtin/{kind}` 与 `GET /api/builtin/{kind}/{id}` 自动列出新资源。

## API

只读目录端点：

| 端点 | 说明 |
|------|------|
| `GET /api/builtin/{kind}` | 列出指定类型的摘要（不含正文），类型：`template`、`soul`、`subagent`、`skill` |
| `GET /api/builtin/{kind}/{id}` | 返回包含正文在内的完整资源 |

`kind` 必须是 `template`、`soul`、`subagent`、`skill` 之一；未知类型或 ID 返回 `404`。
