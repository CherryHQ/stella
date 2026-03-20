---
title: 模型管理
---

## 分层模型

anna 中的每个代理都有三个模型字段，存储在数据库（`settings_agents` 表）中。所有模型字段的格式为 `provider/model`（例如 `anthropic/claude-sonnet-4-6`）。

| 字段           | 使用场景           |
| -------------- | ------------------ |
| `model`        | 代理的默认模型     |
| `model_strong` | 重度推理、复杂任务 |
| `model_fast`   | 快速响应、简单查询 |

`model_strong` 和 `model_fast` 在未设置时都回退到 `model`。通过 admin 面板（`anna --open`）按代理配置这些。

## 提供商设置

提供商通过 admin 面板（`anna --open`）配置。每个提供商都存储在 `settings_providers` 表中，带有可选的 API 密钥和基础 URL。

当提供商的 `api_key` 字段在数据库中为空时，环境变量作为回退：

| 提供商            | 环境变量            | 可选变量          |
| ----------------- | ------------------- | ----------------- |
| Anthropic         | `ANTHROPIC_API_KEY` |                   |
| OpenAI            | `OPENAI_API_KEY`    | `OPENAI_BASE_URL` |
| OpenAI-Compatible | `OPENAI_API_KEY`    | `OPENAI_BASE_URL` |

OpenAI-Compatible 提供商（`openai-response`）支持任何实现 OpenAI Responses API 的服务，例如 Perplexity 或 Together.ai。

## CLI 命令

```bash
anna models             # 列出可用模型（list 的别名）
anna models list        # 列出按提供商分组的所有模型
anna models update      # 从提供商 API 获取模型并更新缓存
anna models current     # 显示活动的 provider/model
anna models set <p/m>   # 切换默认代理的模型（例如 anna models set openai/gpt-4o）
anna models search <q>  # 按名称搜索模型
```

### 模型缓存

`anna models update` 查询所有配置的提供商 API 并将结果保存到 `settings` 表中的 `models_cache` 键下。缓存被 `list`、`search` 和 Telegram 模型选择器使用。

您也可以从 admin 面板刷新缓存。

## 运行时切换

模型可以在运行时切换，无需重启：

- **CLI**：聊天会话期间的 `/model` 命令
- **Telegram**：内联键盘模型选择器
- **CLI 命令**：`anna models set provider/model` 更新数据库中默认代理的模型

## 模型元数据

当填充模型缓存时（通过 `anna models update` 或 admin 面板），每个模型条目都包含从提供商 API 获取的元数据：

- 模型 ID
- 推理能力
- 支持的输入类型（文本、图像）
- 上下文窗口大小
- 最大输出 token
- 每 token 成本（输入、输出、缓存读取、缓存写入）
- 自定义标头

此元数据用于模型解析、显示和成本跟踪。
