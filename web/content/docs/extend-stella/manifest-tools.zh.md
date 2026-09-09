---
title: Agent Plugin 清单
description: 在 plugin.json 中声明 CLI 需求和 Stella 运行时元数据。
---

## 概览

Agent Plugin 包是一个包含标准 `plugin.json` 的目录。Stella 读取清单、包内
skill，以及可选的 `com.cherryhq.stella` 扩展。包读取器只把文件转换为声明，
不会启动进程、安装二进制文件、创建 OAuth 连接，也不会启用 Native 能力。

包的源码布局如下：

```text
plugins/agent/<name>/
  plugin.json
  skills/<skill-name>/SKILL.md
  mcp.json                         # 可选，仅支持 HTTP transport
```

`<name>` 是规范包名，只能使用小写字母、数字、连字符和句点，且不能以分隔符
开头或结尾。只有 `skills/` 的直接子目录中包含 `SKILL.md` 时，读取器才会发现
对应 skill。Stella 会把 skill 的字节内容和文件 mode 保留给后续 asset 层使用。

Native 能力有独立的编译注册和生命周期。包清单不会创建或替换 channel、provider、
hook 或其他 Native 能力。

## 标准清单

标准清单要求 `$schema` 和 `name`。`version`、`description`、`author`、`homepage`、
`repository`、`license` 和 `keywords` 是可选的标准元数据。读取器遇到顶层未知字段
时会产生诊断并在容错读取中忽略；严格 authoring 校验会拒绝它们。

Stella 的声明放在 `com.cherryhq.stella` namespace 下。当前扩展版本为 `"1"`：

```json
{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name": "example-cli",
  "version": "1.0.0",
  "description": "一个示例命令行集成。",
  "extensions": {
    "com.cherryhq.stella": {
      "version": "1",
      "display_name": "Example CLI",
      "prompt": "使用 example-cli 执行示例操作。",
      "binaries": [
        {
          "name": "example",
          "tool": "github:owner/example",
          "version": "1.2.3",
          "options": {
            "asset_pattern": "example_*_linux_x86_64.tar.gz",
            "bin_path": "bin"
          }
        }
      ],
      "session_env": [
        {
          "env_var": "EXAMPLE_ACCESS_TOKEN",
          "source": "oauth.access_token",
          "required": true
        }
      ],
      "oauth": [
        {
          "provider": "example",
          "scopes": ["read"],
          "bindings": [{ "credential": "access_token", "env_var": "EXAMPLE_ACCESS_TOKEN" }]
        }
      ]
    }
  }
}
```

`display_name` 和 `prompt` 是公开的展示与指导字段。它们不能包含凭据，也不会
执行代码。

## Stella 扩展字段

| 字段           | 必填 | 含义                                              |
| -------------- | ---- | ------------------------------------------------- |
| `version`      | 是   | Stella 扩展版本，目前必须为 `"1"`。               |
| `display_name` | 否   | Stella 显示的公开名称。                           |
| `prompt`       | 否   | 与包关联的公开指导文字，是数据而不是可执行代码。  |
| `binaries`     | 否   | 选中会话需要准备的 CLI 声明。                     |
| `session_env`  | 否   | 选中会话的公开环境绑定。                          |
| `oauth`        | 否   | 公开的 provider、scope 以及凭据到环境变量的声明。 |

### 二进制和安装选项

每个 binary 有 `name`、mise `tool` key、可选的 `version`，以及可选的 JSON
`options` 对象。省略 `version` 时，安装器使用 `latest`。`options` 会传给安装器，
并参与 binary 身份计算。安装设置必须放在 `options` 内，不能与它并列。

```json
{
  "name": "gh",
  "tool": "github:cli/cli",
  "version": "2.40.1",
  "options": {
    "bin_path": "bin",
    "strip_components": 1,
    "checksum": "sha256:..."
  }
}
```

常用安装选项类别如下：

| 安装器或用途     | 选项                                                                                              |
| ---------------- | ------------------------------------------------------------------------------------------------- |
| 归档和单文件布局 | `strip_components`、`bin_path`、`bin`、`rename_exe`、`checksum`                                   |
| GitHub release   | `asset_pattern`、`version_prefix`、`no_app`、`filter_bins`、`prerelease`、`api_url`               |
| 直接 HTTP        | `url`、`size`、`format`、`version_list_url`、`version_regex`、`version_json_path`、`version_expr` |
| pipx 和 uvx      | `extras`、`pipx_args`、`uvx`、`uvx_args`                                                          |

具体选项由选中的安装器后端负责。选项不同会产生不同的 binary 身份，也不会复用
不相关的安装。不要把 token、密码、client secret 或 Vault 定位信息放进
`plugin.json` 或 `options`。

支持的 binary tool key 包括 GitHub release（`github:`）、直接 HTTP 下载（`http:`）、
pipx（`pipx:`）、npm（`npm:`）以及托管安装器提供的其他 tool key。清单集成不支持
平台专属的 `platforms` 映射。

### 会话环境

每个 `session_env` 条目声明 `env_var`、`source` 和可选的 `required`。标准条目没有
`value` 字段。`source` 是解析器名称，例如 `oauth.access_token` 或
`oauth.client_id`；凭据会在运行时解析。

OAuth 声明包含公开的 `provider`、可选的 `scopes`，以及绑定数组。每个绑定声明
`credential` 和目标 `env_var`：

```json
{
  "oauth": [
    {
      "provider": "github",
      "bindings": [{ "credential": "access_token", "env_var": "GH_TOKEN" }]
    }
  ]
}
```

OAuth connection binding 暂未实现。MCP 认证应在各个 MCP 子服务上配置。包读取器
不会创建连接，也不会读取凭据。

## 发行版 builtin 和运行时位置

内置 `plugin.json` 定义是不可变的发行资源。管理员可以修改选中范围的启停状态，
也可以覆盖 CLI 的 `binary_versions`；随版本发布的 binary tool、安装选项、公开
元数据和 prompt 仍由发行版拥有。内置 skill 同样由发行版拥有，范围配置不能替换
其成员列表。

只有 `mise` 和 `xberg` 是随发行版嵌入的 runtime。Stella 随版本同步并提取这两个
嵌入式二进制文件。其他 CLI artifact 由服务端后台准备，并通过四种插件范围暴露给
会话：`system`、`system_agent`、`user` 和 `user_agent`。artifact 已准备不等于会话
已经获得访问权，范围解析仍决定会话能看到什么。

`web` 包独立存在，包含 Web skill 所需的 Bun runtime 和 Lightpanda binary。独立的
`bun` 插件开关不会启用或禁用 `web`；应配置 `web` 包及其范围。

## 配置和安装

包定义与范围配置分开保存。范围可以启用或禁用插件，也可以覆盖 CLI binary 版本。
会话启动前，Stella 会为可信 user 和 Agent 解析选中的范围。

如果选中的 binary 不在准备好的缓存中，Stella 会通过托管安装器为目标 runtime 安装。
匹配的准备结果可以复用。Stella 不会把宿主机安装复制进 Docker Linux 沙箱。Native
managed 会话和沙箱会话使用各自的 runtime tree。

包不运行任意安装 hook 或自定义 shell 脚本。安装器只处理声明的 binary。包读取本身
没有进程执行或网络访问路径。

## 限制

- Stella 扩展版本必须严格为 `"1"`。
- `stdio` MCP entry 不支持，会带诊断跳过；请使用支持的 HTTP transport。
- OAuth connection binding 尚未实现。
- 不支持任意安装 hook 和自定义安装脚本。
- 清单和安装选项不得包含 secret 或凭据定位信息。
- 读取器会尽可能在组件边界隔离错误，因此有效 skill 和其他资源仍可加载。严格
  authoring 校验会把未知或不支持的声明报告为错误。
