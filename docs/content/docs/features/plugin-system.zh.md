---
title: 插件系统
---

## 概述

Anna 采用编译内置的插件模型。所有插件都直接编译到 anna 二进制文件中——没有子进程协议、没有独立进程、也不支持第三方插件安装。插件是 `plugins/` 下实现标准接口的 Go 包。

两种插件类型：

- **工具插件**提供 LLM 代理可以调用的工具（如 `webfetch`）。
- **通道插件**提供消息平台集成（如 `telegram`、`qq`、`feishu`、`weixin`）。

注意：核心工具（`read`、`bash`、`edit`、`write`）始终启用，不属于插件。

## 内置插件

Anna 内置了 5 个插件：

| 类型    | 名称       | 描述                     |
| ------- | ---------- | ------------------------ |
| tool    | webfetch   | 获取网页                 |
| channel | telegram   | Telegram 机器人          |
| channel | qq         | QQ 机器人                |
| channel | feishu     | 飞书机器人               |
| channel | weixin     | 微信机器人（通过 iLink） |

## 存储

插件状态存储在数据库的 `settings_plugins` 表中。每个插件包含：

| 字段      | 类型       | 描述                                           |
| --------- | ---------- | ---------------------------------------------- |
| `id`      | string     | 插件 ID（`类型/名称`，如 `tool/webfetch`）     |
| `kind`    | string     | `tool` 或 `channel`                            |
| `name`    | string     | 插件名称                                       |
| `enabled` | bool       | 插件是否启用                                   |
| `config`  | JSON map   | 插件特定配置（令牌、密钥等）                   |

## CLI 命令

```bash
anna plugin list               # 列出所有插件及状态
anna plugin enable <id>        # 启用插件
anna plugin disable <id>       # 禁用插件
anna plugin config <id>        # 查看插件配置
anna plugin config <id> k=v    # 设置插件配置键值对
```

## 管理面板

通道插件（Telegram、QQ、飞书、微信）通过管理面板（`anna --open`）配置。管理面板写入 `settings_plugins` 表，并提供管理令牌、密钥和通道特定设置的界面。
