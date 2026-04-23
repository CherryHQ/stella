---
title: 清单工具插件
description: 从 $ANNA_HOME/plugins.yaml 加载的文件驱动 CLI 工具集成。
---

## 概览

清单工具插件是一种轻量替代方案，无需编写 Go 包，只需在 YAML 文件中声明工具，Anna 会在启动时自动协调二进制文件的下载。

Anna 内置了一个默认清单，声明了标准 CLI 工具集成（`mise`、`tap-web`、`gh`、`lark-cli`）。你可以在 `$ANNA_HOME/plugins.yaml` 中覆盖或扩展这些配置。

## 工作原理

启动时，Anna 会：

1. 加载内嵌的内置清单（`builtin_plugins.yaml`）
2. 如果存在，加载用户清单（`$ANNA_HOME/plugins.yaml`）
3. 合并两者：用户条目按插件 ID 覆盖内置条目
4. 协调已启用的插件：将缺失的二进制文件下载到 `$ANNA_HOME/bin`
5. 将已启用的清单插件注册到插件主机

二进制文件随后可在 Agent 沙箱会话的 `PATH` 中使用。

## 清单文件格式

`$ANNA_HOME/plugins.yaml`：

```yaml
plugins:
  - id: tool/my-cli
    kind: tool
    name: my-cli
    display_name: My CLI
    description: 执行某些有用的操作。
    enabled: true
    binaries:
      - name: my-cli
        repo: owner/my-cli
        version: "1.2.3"   # 省略则使用最新版
    session_env:
      - env_var: MY_TOKEN
        source: static
        value: "abc123"
        required: true
```

## 插件字段

| 字段 | 必填 | 描述 |
|------|------|------|
| `id` | 是 | 唯一插件 ID，格式为 `kind/name`，例如 `tool/my-cli` |
| `kind` | 是 | 插件类型，通常为 `tool` |
| `name` | 是 | 简短的机器可读名称 |
| `display_name` | 否 | 在管理界面显示的人类可读标签 |
| `description` | 否 | 在管理界面显示的简短描述 |
| `enabled` | 否 | 插件是否激活，默认为 false。内置插件默认为 true。 |
| `binaries` | 否 | 需要下载并放置到 `$ANNA_HOME/bin` 的 CLI 二进制文件 |
| `session_env` | 否 | 要注入沙箱会话的环境变量 |

## 二进制字段

| 字段 | 必填 | 描述 |
|------|------|------|
| `name` | 是 | 二进制文件名（不含扩展名） |
| `repo` | 是 | GitHub 仓库，格式为 `owner/repo` |
| `version` | 否 | 要安装的版本标签（如 `"1.2.3"`、`"nightly"`）。默认为 null，由 mise 自行解析。对于没有 `latest` 发布版本的仓库，必须显式指定。 |
| `bin_path` | 否 | 归档文件内包含二进制文件的子目录（如 `"bin"`） |
| `exe` | 否 | 当归档内的二进制文件名与 `name` 不同时进行覆盖 |

mise 会根据发布资产文件名中的操作系统和架构关键词自动检测正确的资产。只有当归档布局或二进制文件名不符合常规时，才需要 `bin_path` 和 `exe`。

## 会话环境变量字段

| 字段 | 必填 | 描述 |
|------|------|------|
| `env_var` | 是 | 环境变量名称 |
| `source` | 是 | 值的解析方式（见下文） |
| `value` | 条件必填 | 当 `source: static` 时使用的字面值 |
| `required` | 否 | 若为 true，则当值无法解析时会话创建失败 |

### 环境变量来源

| 来源 | 描述 |
|------|------|
| `static` | 使用清单中的字面 `value` |
| `github_token` | 注入用户的 GitHub OAuth 令牌 |
| `lark_access_token` | 注入 Lark 用户访问令牌 |
| `lark_app_id` | 注入 Lark 应用 ID |
| `lark_brand` | 注入 Lark 品牌标识符 |

OAuth 来源（`github_token`、`lark_*`）需要在管理面板中配置相应凭证。

## 状态与缓存

Anna 在 `$ANNA_HOME/plugin-manifest-state.json` 中跟踪已安装的二进制版本。后续启动时，版本正确的二进制文件会被跳过。修改 `plugins.yaml` 中的 `version` 字段可触发重新下载。

## 覆盖内置插件

要禁用内置插件，添加一个 `enabled: false` 的条目：

```yaml
plugins:
  - id: tool/mise
    enabled: false
```

要将内置二进制固定到特定版本：

```yaml
plugins:
  - id: tool/tap-web
    enabled: true
    binaries:
      - name: tap
        repo: vaayne/tap
        version: "0.5.0"
```

## 管理界面

管理面板的 Plugins 页面中有一个 **Manifest Tools** 标签页，你可以在此：

- 切换插件的启用/禁用状态
- 触发立即同步以下载或更新二进制文件
- 查看每个插件的同步结果

在界面中切换状态会将覆盖配置写入 `$ANNA_HOME/plugins.yaml`，内嵌的内置清单不会被修改。

## v1 的限制

- 清单不支持系统提示词和技能注册。需要这些功能的插件仍使用 Go 注册。
- 不支持自定义安装脚本，二进制文件必须作为 GitHub 发布资产提供。
- 仅支持 GitHub 发布资产作为二进制来源。
