---
title: 清单工具插件
description: 随服务端一同发布、并可在管理界面中自定义的声明式 CLI 工具集成。
---

## 概览

清单工具插件是一种轻量替代方案：无需编写 Go 包，只要把工具声明为数据。Stella 会把声明规范化为所有后端共用的插件定义与四层范围配置模型。

Stella 内置了一个默认清单，声明了默认由清单管理的 CLI 集成（`gh`、`lark-cli`、`lightpanda`）。它们会显示在对应语义标签页中，例如 **Tools** 或 **Hooks**，并带有 `manifest` 标记。你在 Plugins 管理界面中覆盖或扩展这些配置，改动存入数据库，编译进服务端的清单本身不会被修改。

## 工作原理

启动时，Stella 会：

1. 加载内嵌的内置清单（`resources/oauth.yaml` 和 `resources/tools.yaml`）
2. 从数据库读取已存储的自定义并覆盖到内置定义之上，同时追加没有内置定义支撑的、由你创建的插件
3. 将定义和范围配置规范化到公共插件 catalog
4. 将已启用的清单插件注册到插件主机

启动不会下载二进制。Runner 在会话需要时，按捕获的插件 snapshot 惰性物化选中的二进制。Native managed 会话使用 managed tree；user 和 user-agent 选择使用各自的沙箱目录。Docker 在自己的边界内准备 Linux 原生文件。

## Docker 沙箱中的 CLI 可用性

不要把宿主机 `$STELLA_HOME/bin` 当作 Docker 沙箱可执行文件的来源。在 macOS 和 Windows 上，
Native managed 安装会产生宿主机平台的二进制文件，它们无法在 Linux 容器中运行。把该目录
绑定挂载进 Docker 也会模糊宿主机工具管理和容器运行时之间的边界。

对于 Docker：

- 必须开箱即用的内置 CLI 插件会预装到带版本的沙箱镜像中。沙箱镜像标签与 Stella release 绑定，因此一个 release 镜像可以包含该 Stella 版本对应的内置工具集合。镜像从精确的 release 声明构建；运行时不维护第二份 Docker manifest，也不执行守护进程级 builtin installer。
- 解析后的清单（内置定义加上已存储的自定义）仍然是插件元数据、启用状态、会话环境变量、OAuth 注入以及本地沙箱二进制安装的来源。
- 用户配置的 CLI 二进制需要一条容器原生的加载路径。它们应在 Docker 环境内按 Linux 目标安装，而不是从宿主机 `$STELLA_HOME/bin` 复制。

一种用于用户配置 CLI 的安全 Docker 加载设计是：

1. 按 runner 可信的 user 和 Agent 解析完整四层插件选择。
2. 把该选择交给现有容器 preparation helper，只安装选中的 Linux 文件。
3. 将结果保存到 Docker 工具缓存或 volume，缓存键由一个解析后的 image ID 加完整选择身份组成。
4. 将选中的条目挂载到沙箱会话中的容器专用路径，并前置到容器内 `PATH`。
5. 捕获的 snapshot 或 image ID 变化时准备新的选择。

这样可以保持 release 沙箱镜像稳定，同时仍支持用户新增 CLI。安装得到的用户二进制是 Linux 容器二进制，宿主机 `$STELLA_HOME/bin` 不参与 Docker 可执行文件解析。`none` backend 仍是在可信宿主机上执行，不提供文件系统隔离。

## 插件定义

无论是随 `resources/tools.yaml` 发布，还是在管理界面里填写，清单插件都是同一组字段。下面的 YAML 形式是阅读这个结构最清楚的方式；管理界面编辑的是同样这些字段，只是呈现为表单行。

Manifest 提供 `PluginDefinition`；启用状态和配置以四种范围 `system`、
`system_agent`、`user`、`user_agent` 的 `PluginConfig` 保存。选中的范围拥有完整
后端决策。System 或匹配 system-agent 的显式 `false` 是上限，禁用的胜出项不会
回退到更宽范围。Builtin 定义和资源由发行版拥有，但任何 builtin 插件都可以禁用。
CLI 版本 pin 与 Skill 来源保持独立。

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
| `enabled`        | 否   | 清单输入的简写，会规范化到选中的 `PluginConfig`；不是第二套权限系统。 |
| `binaries`       | 否   | CLI 二进制声明；Native managed 安装使用 managed tree，用户范围使用各自的沙箱目录 |
| `session_env`    | 否   | 要注入沙箱会话的环境变量                                              |
| `oauth_provider` | 否   | `oauth.*` 会话环境变量来源使用的静态 OAuth provider ID，例如 `github` |

## 二进制字段

每个二进制需要 `name` 和 `tool` 字段。`tool` 字段使用 mise 的工具键格式：`backend:identifier`。

### 公共字段

| 字段               | 必填 | 描述                                                            |
| ------------------ | ---- | --------------------------------------------------------------- |
| `name`             | 是   | 在选中运行时目录中暴露的二进制文件名（不含扩展名）              |
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

| 来源                 | 描述                                         |
| -------------------- | -------------------------------------------- |
| `static`             | 使用清单中的字面 `value`                     |
| `oauth.access_token` | 注入已连接 provider 的 OAuth access token    |
| `oauth.client_id`    | 注入已连接 provider 令牌包中的 client/app ID |

`oauth.*` 来源会通过插件的 `oauth_provider` 解析。GitHub 使用 Stella 内置的 GitHub CLI 设备流程应用，无需管理员配置插件。其他 provider 必须另行声明和配置。

## 状态与缓存

Stella 在 `$STELLA_HOME/plugin-manifest-state.json` 中跟踪已安装的二进制版本。选中的 snapshot 尽可能复用正确版本；修改二进制的 `version` 会触发新的惰性安装。Preparation 会随会话取消，Stella 也会终止安装器派生出的子进程。

## 管理界面

清单驱动的插件只显示一次，并出现在符合其类型的标签页中：

- `tool/gh`、`tool/lark-cli` 和 `tool/lightpanda` 显示在 **Tools**。

由清单管理的行会显示 `manifest` 标记，并提供 **Edit definition** 操作用于编辑插件定义。二进制文件和会话环境变量会以表单行编辑。如果同一个插件还提供运行时配置，该行也会显示 **Configure**。启用开关与定义分开存储，因此禁用内置插件不算自定义；而把某个二进制固定到指定版本，则是一次普通的定义编辑。

**Tools** 标签页提供 **Add Tool**，可以从 GitHub release 二进制创建新的清单 CLI 工具。保存后会注册插件；下一个符合条件的 runner 会按自己的选择惰性物化二进制，无需重启。内嵌的内置清单不会被修改。

在管理界面里编辑内置插件时，只会存储你改动过的字段，其余字段继续跟随服务端自带的定义，升级后仍会随之更新。这类插件会标记为 **已自定义**，并提供 **恢复默认**：丢弃已存储的改动，启用开关保持不变。列表类字段（二进制文件、技能、会话环境变量）整体存储 —— 改了其中一个二进制，就等于接管了整个列表。在此行为出现之前保存的自定义存的是一整份定义，会一直冻结在那个版本；重新保存一次该插件即可改写为新格式，之后便会继续跟随升级。

## v1 的限制

- 清单不支持系统提示词和技能注册。需要这些功能的插件仍使用 Go 注册。
- 清单没有独立的运行时或同步 endpoint；二进制安装由解析后的插件 snapshot 驱动。
- 不支持自定义安装脚本。
- 不支持平台特定资产模式（`platforms:` 映射），请改用 `asset_pattern`。
- 支持的二进制来源：GitHub 发布（`github`）、直接 HTTP 下载（`http`）、pipx（`pipx`）、npm（`npm`）。
