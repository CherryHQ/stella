---
title: 清单工具插件
description: 从 $STELLA_HOME/plugins.yaml 加载的文件驱动 CLI 工具集成。
---

## 概览

清单工具插件是一种轻量替代方案，无需编写 Go 包，只需在 YAML 文件中声明工具，或从 Plugins 管理界面添加工具，Stella 会自动协调二进制文件的下载。

Stella 内置了一个默认清单，声明了默认由清单管理的 CLI 集成（`tap-web`、`gh`、`lark-cli`、`rtk`）。它们会显示在对应语义标签页中，例如 **Tools** 或 **Hooks**，并带有 `manifest` 标记。你可以在 `$STELLA_HOME/plugins.yaml` 或管理界面中覆盖或扩展这些配置。

## 工作原理

启动时，Stella 会：

1. 加载内嵌的内置清单（`resources/oauth.yaml` 和 `resources/tools.yaml`）
2. 如果存在，加载用户清单（`$STELLA_HOME/plugins.yaml`）
3. 合并两者：用户条目按插件 ID 覆盖内置条目
4. 将已启用的清单插件注册到插件主机
5. 在后台启动二进制协调：将缺失的二进制文件下载到 `$STELLA_HOME/bin`

启动不会被二进制下载阻塞。新增或更新的清单二进制会在后台同步完成后，出现在 Agent 沙箱会话的 `PATH` 中。对于本地沙箱会话，二进制文件通过 `$STELLA_HOME/bin` 提供。Docker 沙箱会话需要单独处理，因为宿主机二进制可能面向宿主机 OS/架构，而不是 Linux。

## Docker 沙箱中的 CLI 可用性

不要把宿主机 `$STELLA_HOME/bin` 当作 Docker 沙箱可执行文件的来源。在 macOS 和 Windows 上，清单同步可能安装宿主机平台的二进制文件，它们无法在 Linux 容器中运行。把该目录绑定挂载进 Docker 也会模糊宿主机工具管理和容器运行时之间的边界。

对于 Docker：

- 必须开箱即用的内置 CLI 插件会预装到带版本的沙箱镜像中。沙箱镜像标签与 Stella release 绑定，因此一个 release 镜像可以包含该 Stella 版本对应的内置工具集合。镜像构建时运行 `stellad mise reconcile-builtins`（与守护进程相同的 reconcile 流程），按 `resources/tools.yaml` 声明的标识符与版本安装，无需再单独维护一份 Docker 工具列表。
- `$STELLA_HOME/plugins.yaml` 仍然是插件元数据、启用状态、会话环境变量、OAuth 注入以及本地沙箱二进制安装的来源。
- 用户配置的 CLI 二进制需要一条容器原生的加载路径。它们应在 Docker 环境内按 Linux 目标安装，而不是从宿主机 `$STELLA_HOME/bin` 复制。

一种用于用户配置 CLI 的安全 Docker 加载设计是：

1. 从已启用的用户清单插件 `binaries` 条目生成容器工具清单，排除 release 镜像中已经存在的内置工具。
2. 基于同一个沙箱镜像启动短生命周期 helper 容器，在 Linux 上下文中运行 `mise install`。
3. 将安装结果保存到由 Docker 管理的工具缓存或 volume，并用沙箱镜像标签加用户清单哈希作为缓存键。
4. 将该缓存挂载到沙箱会话中的容器专用路径，并前置到容器内 `PATH`。
5. 当已启用的用户插件集合或二进制版本变化时，重建或刷新缓存。

这样可以保持 release 沙箱镜像稳定，同时仍支持用户新增 CLI。安装得到的用户二进制是 Linux 容器二进制，宿主机 `$STELLA_HOME/bin` 不参与 Docker 可执行文件解析。

## 清单文件格式

`$STELLA_HOME/plugins.yaml`：

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
        tool: github:owner/my-cli
        version: "1.2.3" # 省略则使用最新版
    session_env:
      - env_var: MY_TOKEN
        source: static
        value: "abc123"
        required: true
```

## 插件字段

| 字段             | 必填 | 描述                                                                  |
| ---------------- | ---- | --------------------------------------------------------------------- |
| `id`             | 是   | 唯一插件 ID，格式为 `kind/name`，例如 `tool/my-cli`                   |
| `kind`           | 是   | 插件类型，通常为 `tool`                                               |
| `name`           | 是   | 简短的机器可读名称                                                    |
| `display_name`   | 否   | 在管理界面显示的人类可读标签                                          |
| `description`    | 否   | 在管理界面显示的简短描述                                              |
| `enabled`        | 否   | 插件是否激活，默认为 false。内置插件默认为 true。                     |
| `binaries`       | 否   | 需要下载并放置到 `$STELLA_HOME/bin` 的 CLI 二进制文件                 |
| `session_env`    | 否   | 要注入沙箱会话的环境变量                                              |
| `oauth_provider` | 否   | `oauth.*` 会话环境变量来源使用的静态 OAuth provider ID，例如 `github` |

## 二进制字段

每个二进制需要 `name` 和 `tool` 字段。`tool` 字段使用 mise 的工具键格式：`backend:identifier`。

### 公共字段

| 字段               | 必填 | 描述                                                            |
| ------------------ | ---- | --------------------------------------------------------------- |
| `name`             | 是   | 放置到 `$STELLA_HOME/bin` 的二进制文件名（不含扩展名）          |
| `tool`             | 是   | mise 工具键，格式为 `backend:identifier`（如 `github:cli/cli`） |
| `version`          | 否   | 要安装的版本，默认为 `latest`。                                 |
| `strip_components` | 否   | 解压归档时去除的前导目录层数，大多数布局可自动检测。            |
| `bin_path`         | 否   | 归档内包含二进制的子目录（如 `"bin"`）。                        |
| `bin`              | 否   | 当资产为单个二进制（非归档）时重命名下载文件。                  |
| `rename_exe`       | 否   | 从归档提取后重命名可执行文件。                                  |
| `checksum`         | 否   | 以 `algo:hex` 格式验证资产校验和（如 `"sha256:abc123..."`）。   |

### GitHub 后端（`github:owner/repo`）

```yaml
binaries:
  - name: gh
    tool: github:cli/cli
    version: "2.40.1"
    bin_path: bin
```

| 字段             | 描述                                                                            |
| ---------------- | ------------------------------------------------------------------------------- |
| `asset_pattern`  | 选择发布资产的 glob 模式（如 `"gh_*_linux_x64.tar.gz"`）。                      |
| `version_prefix` | 标签自定义前缀（如 `"release-"`）。                                             |
| `no_app`         | 跳过 macOS `.app` 包，优先使用独立二进制。                                      |
| `filter_bins`    | 当归档含多个可执行文件时，逗号分隔的 PATH 可见二进制列表。                      |
| `prerelease`     | 解析 `latest` 时包含预发布版本。                                                |
| `api_url`        | GitHub Enterprise 的 API 基础 URL（如 `"https://github.example.com/api/v3"`）。 |

### HTTP 后端（`http:name`）

`http:` 后的标识符是 mise 内部使用的工具名称。

```yaml
binaries:
  - name: sentinel
    tool: http:sentinel
    url: "https://releases.hashicorp.com/sentinel/{{version}}/sentinel_{{version}}_{{os()}}_{{arch()}}.zip"
    version: "0.26.3"
```

| 字段                | 描述                                                                         |
| ------------------- | ---------------------------------------------------------------------------- |
| `url`               | 下载 URL，http 后端必填，支持 `{{version}}`、`{{os()}}`、`{{arch()}}` 模板。 |
| `size`              | 用于验证的预期文件大小（字节）。                                             |
| `format`            | 归档格式覆盖（如 `"tar.xz"`）。                                              |
| `version_list_url`  | 获取可用版本列表的 URL。                                                     |
| `version_regex`     | 从版本列表中提取版本号的正则表达式。                                         |
| `version_json_path` | 从 JSON 中提取版本的 jq 风格路径（如 `".[].tag_name"`）。                    |
| `version_expr`      | 提取版本的 expr-lang 表达式。                                                |

### Pipx 后端（`pipx:package`）

标识符是 PyPI 包名、`org/repo` GitHub 格式，或 `git+https://...`。

```yaml
binaries:
  - name: mypy
    tool: pipx:mypy
    version: "1.8.0"
```

| 字段        | 描述                                     |
| ----------- | ---------------------------------------- |
| `extras`    | 随包安装的 pip extras。                  |
| `pipx_args` | 传递给 pipx 的额外参数。                 |
| `uvx`       | 使用 `uvx`（uv 的工具运行器）代替 pipx。 |
| `uvx_args`  | uvx 的额外参数。                         |

### NPM 后端（`npm:package`）

```yaml
binaries:
  - name: serve
    tool: npm:serve
    version: "14.2.0"
```

平台特定资产模式（`platforms:` 映射）在清单中不受支持。

## 会话环境变量字段

| 字段       | 必填     | 描述                                    |
| ---------- | -------- | --------------------------------------- |
| `env_var`  | 是       | 环境变量名称                            |
| `source`   | 是       | 值的解析方式（见下文）                  |
| `value`    | 条件必填 | 当 `source: static` 时使用的字面值      |
| `required` | 否       | 若为 true，则当值无法解析时会话创建失败 |

### 环境变量来源

| 来源                 | 描述                                               |
| -------------------- | -------------------------------------------------- |
| `static`             | 使用清单中的字面 `value`                           |
| `oauth.access_token` | 注入已连接 provider 的 OAuth access token          |
| `oauth.client_id`    | 注入已连接 provider 令牌包中的 client/app ID       |
| `oauth.brand`        | 注入已连接 provider 令牌包中的品牌标识（如果存在） |

`oauth.*` 来源会通过插件的 `oauth_provider` 解析。GitHub 使用 Stella 内置的 GitHub CLI 设备流程应用，无需管理员配置插件。包括飞书/Lark 在内的其他 provider 需要在 Web UI 对应的 OAuth provider 卡片中配置。内置 lark-cli 不声明 `oauth_provider` 或 OAuth session env，而是使用自己的原生授权。

## 状态与缓存

Stella 在 `$STELLA_HOME/plugin-manifest-state.json` 中跟踪已安装的二进制版本。后续启动时，版本正确的二进制文件会被跳过。修改 `plugins.yaml` 中的 `version` 字段可触发重新下载。启动时的协调会在后台运行，并会在关闭时取消；Stella 也会终止安装器派生出的子进程。

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
        tool: github:vaayne/tap
        version: "0.5.0"
```

内置插件覆盖是完整条目替换。如果为了修改某个字段而覆盖内置插件，需要把仍然需要的其他字段也一并写上。

### lark-cli 原生授权

内置 `tool/lark-cli` 是普通 CLI 工具，不是 Stella OAuth 消费者。Stella 从当前 Agent 唯一启用的飞书/Lark Channel 获取 `feishu` 或 `lark`、App ID 和 App Secret，并在每个“员工 × Agent”私有工作区中初始化 lark-cli；员工 scope 和 token 由 lark-cli 原生设备授权管理。不要通过 manifest override 把 `oauth_provider` 或 OAuth token env 再加回来。

## 管理界面

清单驱动的插件只显示一次，并出现在符合其类型的标签页中：

- `tool/gh`、`tool/lark-cli` 和 `tool/tap-web` 显示在 **Tools**。
- `hook/rtk` 显示在 **Hooks**。

由清单管理的行会显示 `manifest` 标记，并提供 **Edit definition** 操作用于编辑 YAML 支持的插件定义。二进制文件和会话环境变量会以表单行编辑。如果同一个插件还提供运行时配置，该行也会显示 **Configure**。

**Tools** 标签页提供 **Add Tool**，可以从 GitHub release 二进制创建新的清单 CLI 工具。保存后会写入 `$STELLA_HOME/plugins.yaml`，注册插件，并自动同步二进制文件，无需重启。内嵌的内置清单不会被修改。

## v1 的限制

- 清单不支持系统提示词和技能注册。需要这些功能的插件仍使用 Go 注册。
- 不支持自定义安装脚本。
- 不支持平台特定资产模式（`platforms:` 映射），请改用 `asset_pattern`。
- 支持的二进制来源：GitHub 发布（`github`）、直接 HTTP 下载（`http`）、pipx（`pipx`）、npm（`npm`）。
