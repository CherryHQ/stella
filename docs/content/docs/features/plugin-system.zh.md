---
title: 插件系统
---

## 概述

Anna 采用编译内置的插件模型。所有插件都直接编译到 anna 二进制文件中——没有子进程协议、没有独立进程、也不支持第三方插件安装。插件是 `plugins/` 下通过 `init()` 自注册并实现标准接口的 Go 包。

四种插件类型：

- **工具插件**提供 LLM 代理可以调用的工具（如 `webfetch`）。
- **通道插件**提供消息平台集成（如 `telegram`、`qq`、`feishu`、`weixin`）。
- **钩子插件**拦截引擎生命周期事件（如工具调用前后、LLM 调用前后）。
- **供应商插件**提供 LLM API 适配器（如 `anthropic`、`openai`、`openai-response`）。

注意：核心工具（`read`、`bash`、`edit`、`write`）始终启用，不属于插件。

## 内置插件

Anna 内置了 9 个插件：

| 类型     | 名称            | 描述                                   |
| -------- | --------------- | -------------------------------------- |
| tool     | webfetch        | 获取网页                               |
| channel  | telegram        | Telegram 机器人                        |
| channel  | qq              | QQ 机器人                              |
| channel  | feishu          | 飞书机器人                             |
| channel  | weixin          | 微信机器人（通过 iLink）               |
| hook     | rtk             | 请求追踪和费用日志                     |
| provider | anthropic       | Anthropic Messages API（Claude 模型）  |
| provider | openai          | OpenAI Chat Completions API（GPT 模型）|
| provider | openai-response | OpenAI Responses API（兼容服务）       |

## 插件架构

所有插件类型遵循相同模式：

1. 每个插件是 `plugins/{kind}/{name}/` 下的 Go 包
2. 包的 `init()` 函数调用对应类型注册表的 `Register()` 方法
3. `plugins/all.go` 中的空白导入在启动时触发注册
4. 注册表的 `BuildEnabled()`（供应商使用 `BuildAll()`）在运行时实例化活跃插件

```
plugins/
├── all.go                          # 空白导入触发 init() 注册
├── tools/
│   ├── registry.go                 # 工具插件注册表
│   └── webfetch/                   # 工具：网页获取
├── channels/
│   ├── telegram/                   # 通道：Telegram 机器人
│   ├── qq/                         # 通道：QQ 机器人
│   ├── feishu/                     # 通道：飞书机器人
│   └── weixin/                     # 通道：微信机器人
├── hooks/
│   ├── registry.go                 # 钩子插件注册表
│   └── rtk/                        # 钩子：请求追踪
└── providers/
    ├── registry.go                 # 供应商插件注册表
    ├── anthropic/                  # 供应商：Anthropic API
    ├── openai/                     # 供应商：OpenAI Chat Completions
    └── openai-response/            # 供应商：OpenAI Responses API
```

### 添加新插件

要添加新插件，在相应的 `plugins/{kind}/` 目录下创建一个包含 `init()` 函数的包，在其中向对应类型的注册表注册。然后在 `plugins/all.go` 中添加空白导入。无需其他连接代码。

示例——添加新的供应商：

```go
// plugins/providers/gemini/client.go
package gemini

import (
    pluginproviders "github.com/vaayne/anna/plugins/providers"
    "github.com/vaayne/anna/internal/ai"
)

func init() {
    pluginproviders.Register("gemini", pluginproviders.ProviderMeta{
        Name:       "Google Gemini",
        DefaultURL: "https://generativelanguage.googleapis.com",
    }, func(cfg pluginproviders.ProviderConfig) ai.ProviderAdapter {
        return New(Config{APIKey: cfg.APIKey, BaseURL: cfg.BaseURL})
    })
}
```

## 存储

插件状态存储在数据库的 `settings_plugins` 表中。每个插件包含：

| 字段      | 类型       | 描述                                           |
| --------- | ---------- | ---------------------------------------------- |
| `id`      | string     | 插件 ID（`类型/名称`，如 `tool/webfetch`）     |
| `kind`    | string     | `tool`、`channel`、`hook` 或 `provider`        |
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

通道插件和供应商插件通过管理面板（`anna --open`）配置。管理面板写入 `settings_plugins` 表，并提供管理令牌、密钥和插件特定设置的界面。供应商插件会在供应商页面动态显示——添加新的供应商插件会自动出现在管理界面的下拉列表中。
