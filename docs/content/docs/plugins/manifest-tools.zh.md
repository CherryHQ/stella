---
title: 清单工具插件
description: 从 $ANNA_HOME/plugins.yaml 加载的文件驱动 CLI 工具集成。
---

## 概览

清单工具插件是一种轻量替代方案，无需编写 Go 包，只需在 YAML 文件中声明工具，或从 Plugins 管理界面添加工具，Anna 会自动协调二进制文件的下载。

Anna 内置了一个默认清单，声明了默认由清单管理的 CLI 集成（`tap-web`、`gh`、`lark-cli`、`rtk`）。它们会显示在对应语义标签页中，例如 **Tools** 或 **Hooks**，并带有 `manifest` 标记。你可以在 `$ANNA_HOME/plugins.yaml` 或管理界面中覆盖或扩展这些配置。

## 工作原理

启动时，Anna 会：

1. 加载内嵌的内置清单（`builtin_plugins.yaml`）
2. 如果存在，加载用户清单（`$ANNA_HOME/plugins.yaml`）
3. 合并两者：用户条目按插件 ID 覆盖内置条目
4. 将已启用的清单插件注册到插件主机
5. 在后台启动二进制协调：将缺失的二进制文件下载到 `$ANNA_HOME/bin`

启动不会被二进制下载阻塞。新增或更新的清单二进制会在后台同步完成后，出现在 Agent 沙箱会话的 `PATH` 中。

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
| `oauth_provider` | 否 | `oauth.*` 会话环境变量来源使用的静态 OAuth provider ID，例如 `github` |
| `oauth_provider_config_field` | 否 | 用于动态选择 OAuth provider 的插件配置字段，例如 `brand` |
| `oauth_provider_choices` | 条件必填 | 设置 `oauth_provider_config_field` 时允许选择的 provider ID |

## 二进制字段

| 字段 | 必填 | 描述 |
|------|------|------|
| `name` | 是 | 二进制文件名（不含扩展名） |
| `repo` | 是 | GitHub 仓库，格式为 `owner/repo` |
| `version` | 否 | 要安装的版本标签（如 `"1.2.3"`、`"nightly"`）。默认为 `latest`。对于没有 `latest` 发布版本的仓库，必须显式指定。 |
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
| `oauth.access_token` | 注入已连接 provider 的 OAuth access token |
| `oauth.client_id` | 注入已连接 provider 令牌包中的 client/app ID |
| `oauth.brand` | 注入已连接 provider 令牌包中的品牌标识（如果存在） |

`oauth.*` 来源会通过插件的 `oauth_provider` 解析。GitHub 使用 Anna 内置的 GitHub CLI 设备流程应用，无需管理员配置插件。飞书/Lark 来源仍需要先在管理面板中配置 Lark CLI 插件凭据。

## 状态与缓存

Anna 在 `$ANNA_HOME/plugin-manifest-state.json` 中跟踪已安装的二进制版本。后续启动时，版本正确的二进制文件会被跳过。修改 `plugins.yaml` 中的 `version` 字段可触发重新下载。启动时的协调会在后台运行，并会在关闭时取消；Anna 也会终止安装器派生出的子进程。

## 覆盖内置插件

要禁用内置插件，添加一个 `enabled: false` 的条目：

```yaml
plugins:
  - id: tool/tap-web
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

内置插件覆盖是完整条目替换。如果为了修改某个字段而覆盖内置插件，需要把仍然需要的其他字段也一并写上。

### 在飞书和 Lark 之间切换 `lark-cli`

内置 `tool/lark-cli` 清单不会把飞书或 Lark 写死两遍。它会从插件的 `brand` 配置字段解析 OAuth provider，并从保存的 OAuth 令牌包注入品牌：

```yaml
oauth_provider_config_field: brand
oauth_provider_choices: [lark, feishu]
session_env:
  - env_var: LARKSUITE_CLI_BRAND
    source: oauth.brand
```

在 Lark CLI 插件设置中配置 `brand`：

- `feishu` 使用飞书 OAuth，并注入 `LARKSUITE_CLI_BRAND=feishu`。
- `lark` 使用国际版 Lark OAuth，并注入 `LARKSUITE_CLI_BRAND=lark`。

不要仅为了在飞书和 Lark 之间切换而覆盖清单。只有在需要修改二进制或环境变量声明本身时，才需要覆盖 manifest。

## 管理界面

清单驱动的插件只显示一次，并出现在符合其类型的标签页中：

- `tool/gh`、`tool/lark-cli` 和 `tool/tap-web` 显示在 **Tools**。
- `hook/rtk` 显示在 **Hooks**。

由清单管理的行会显示 `manifest` 标记，并提供 **Edit definition** 操作用于编辑 YAML 支持的插件定义。二进制文件和会话环境变量会以表单行编辑。如果同一个插件还提供运行时配置，例如 Lark CLI 的凭据和 `brand`，该行也会显示 **Configure**。

**Tools** 标签页提供 **Add Tool**，可以从 GitHub release 二进制创建新的清单 CLI 工具。保存后会写入 `$ANNA_HOME/plugins.yaml`，注册插件，并自动同步二进制文件，无需重启。内嵌的内置清单不会被修改。

## v1 的限制

- 清单不支持系统提示词和技能注册。需要这些功能的插件仍使用 Go 注册。
- 不支持自定义安装脚本，二进制文件必须作为 GitHub 发布资产提供。
- 仅支持 GitHub 发布资产作为二进制来源。
