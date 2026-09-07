---
title: Agent Plugin 包参考
---

Agent Plugin 读取器加载可移植包声明。发行生成器与 Agent Plugin 服务将这些声明
作为一个包含独立资源种类的包使用。单独读取包不会安装二进制文件、启用 Native
能力、创建 OAuth 连接或启动进程。

## 可移植目录

包根目录必须有 `plugin.json`。Skill 只从 `skills/` 的直接子目录发现，且
该目录必须包含有效 Agent Skills 文档 `SKILL.md`。MCP server 只从包根的
`mcp.json` 读取。

Stella 本地支持 Agent Plugins 1.0.0。读取包时不会联网获取 schema。
`plugin.json` 顶层未知字段会产生诊断并被忽略。未知 extension namespace
是不透明数据，不产生诊断，读取器也不会解释其内容。

包内路径会在解析后的包根目录内解析。指向包内目标的 symlink 允许；解析
到包外的 symlink 会在最窄的组件边界拒绝。读取器保留 `SKILL.md` 的原始
字节和文件 mode，供后续 asset 层使用。

## `com.cherryhq.stella` 扩展

Stella 声明位于 `plugin.json.extensions["com.cherryhq.stella"]`，必须显式
提供扩展 `version`。当前支持的声明组如下：

| 字段           | 含义                                                                                                           |
| -------------- | -------------------------------------------------------------------------------------------------------------- |
| `display_name` | Web UI 展示的可选公共名称。                                                                                    |
| `prompt`       | 选中包时包含的可选公共提示。                                                                                   |
| `binaries`     | 公共命令、安装工具、可选版本和安装选项。                                                                       |
| `session_env`  | 运行时变量、公共 source 标识和是否必需。                                                                       |
| `oauth`        | 公共 provider 标识、请求 scope，以及凭据到环境变量的绑定。暂不支持 connection 绑定；MCP 认证在各子服务上配置。 |

扩展 `version` 当前必须严格为 `"1"`。Skill 的标准 `compatibility` 字段可
用人类可读文字说明环境或 native capability 需求；运行时 policy 仍决定
capability 是否可用。

下面的最小声明可通过严格 authoring 校验：

```json
{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name": "example.tools",
  "extensions": {
    "com.cherryhq.stella": {
      "version": "1",
      "binaries": [{ "name": "gh", "tool": "github:cli/cli" }],
      "session_env": [{ "env_var": "GH_TOKEN", "source": "oauth.access_token" }],
      "oauth": [
        {
          "provider": "github",
          "scopes": ["repo"],
          "bindings": [{ "credential": "access_token", "env_var": "GH_TOKEN" }]
        }
      ]
    }
  }
}
```

这些字段是声明，不是实现。包中不得包含 token、client secret、数据库配
置 UUID、Vault 定位信息、安装状态，也不得包含 native tool/channel/provider/
hook 实现。扩展读取器没有进程执行或网络访问路径。

## MCP transport 边界

读取器保留 streamable HTTP（`streamable-http`）和 legacy HTTP+SSE
（`sse`）entry，每个 entry 的 URL 和可见 header 独立保存。一个 entry
损坏不会隐藏其他有效 server。读取器会识别 `stdio`，给出 unsupported 诊断
后跳过；Stella 绝不会为兼容性回退启动包内进程。unsupported transport 和
格式错误只影响对应组件，包内有效 skill 与其他 server 仍可加载。

Authoring 校验比客户端读取更严格：未知 manifest 字段、unsupported stdio、
无效组件和错误的 Stella 声明都会成为 authoring error。容错读取会保留独
立有效组件，并只对能够安全解释的问题返回诊断。

## 发布与会话约束

发布源码只接受 `plugin.json`，拒绝包根目录中的 `plugin.yaml` 和 `assets.yaml`。
生成的 YAML 只是内嵌数据格式。历史数据库升级保留导入路径，不恢复旧作者格式
或旧管理 API。

同一会话选中的包必须使用不同的环境变量名。两个包声明同名变量时，即使
指定同一个 provider，也会在注入凭据前拒绝准备会话。

Plugin 定义和配置写入请求的 JSON 请求体上限为 1 MiB，超限返回 HTTP 400。
