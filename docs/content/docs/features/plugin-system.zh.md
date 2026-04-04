---
title: 插件系统
---

## 概述

Anna 采用统一的子进程插件模型。每个插件（包括内置和用户安装的）都是一个独立进程，通过版本化的 stdio 协议（JSON-RPC 风格）与 anna 通信。没有 JavaScript 插件或进程内钩子。

两种插件类型：

- **工具插件**提供 LLM 代理可以调用的工具（如 `read`、`bash`、`edit`、`write`、`webfetch`）。
- **通道插件**提供消息平台集成（如 `telegram`、`qq`、`feishu`、`weixin`）。

## 内置插件

Anna 内置了 9 个插件，编译在二进制文件中：

| 类型    | 名称       | 描述                     |
| ------- | ---------- | ------------------------ |
| tool    | read       | 读取文件                 |
| tool    | bash       | 执行 shell 命令          |
| tool    | edit       | 编辑文件（搜索和替换）   |
| tool    | write      | 写入文件                 |
| tool    | webfetch   | 获取网页                 |
| channel | telegram   | Telegram 机器人          |
| channel | qq         | QQ 机器人                |
| channel | feishu     | 飞书机器人               |
| channel | weixin     | 微信机器人（通过 iLink） |

内置插件使用与用户安装插件相同的子进程协议。可以通过安装同名插件来替换它们。

## 插件清单

每个插件由 `plugin.json` 清单定义：

```json
{
  "name": "my-tool",
  "version": "1.0.0",
  "kind": "tool",
  "protocol_version": "1",
  "description": "插件功能描述。",
  "entrypoint": "./my-tool-binary",
  "tools": [
    {
      "name": "my_tool",
      "description": "提供给 LLM 的工具描述。",
      "input_schema": {
        "type": "object",
        "properties": {
          "query": { "type": "string", "description": "搜索查询" }
        }
      }
    }
  ]
}
```

## 存储

插件状态存储在数据库的 `settings_plugins` 表中。每个插件包含：

| 字段      | 类型       | 描述                                           |
| --------- | ---------- | ---------------------------------------------- |
| `id`      | string     | 插件 ID（`类型/名称`，如 `tool/read`）         |
| `kind`    | string     | `tool` 或 `channel`                            |
| `name`    | string     | 插件名称                                       |
| `enabled` | bool       | 插件是否启用                                   |
| `config`  | JSON map   | 插件特定配置（令牌、密钥等）                   |

## CLI 命令

```bash
anna plugin list               # 列出所有插件及状态
anna plugin add <path>         # 从包含 plugin.json 的目录安装插件
anna plugin remove <name>      # 删除已安装的插件（别名：rm）
anna plugin enable <name>      # 启用插件
anna plugin disable <name>     # 禁用插件
anna plugin config <name>      # 查看/设置插件配置
```

`add` 命令将插件目录复制到 `~/.anna/plugins/installed/` 并在数据库中注册。`remove` 命令删除条目和安装文件。

## 用户安装的插件

用户插件安装在 `~/.anna/plugins/installed/<name>/`。每个目录必须包含 `plugin.json` 清单和入口二进制文件或脚本。

安装插件：

```bash
anna plugin add /path/to/my-plugin
```

这会将插件复制到安装目录，在数据库中注册并启用它。插件在下次 anna 启动时加载。

## 协议

插件通过 stdin/stdout 使用基于 JSON 的协议与 anna 通信：

1. **宿主发送请求**（stdin 上的 JSON 行）：`{"method": "execute", "params": {"tool": "my_tool", "input": {...}}}`
2. **插件发送响应**（stdout 上的 JSON 行）：`{"result": "工具输出文本"}` 或 `{"error": "错误消息"}`

插件的 stderr 转发到 anna 的结构化日志中。

## 安全模型

- 插件在进程外运行——崩溃不会导致 anna 主守护进程崩溃。
- 子进程插件受到监管，在适当时会重新启动。
- 工具插件通过路径验证限制在允许的目录中。
- 插件的 stderr 被捕获并转发到 anna 的结构化日志中。
