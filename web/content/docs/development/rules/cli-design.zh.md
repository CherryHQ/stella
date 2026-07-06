---
title: CLI 设计规则
description: Stella cmd/stella 命令行界面约定。
---

> 这是给贡献者看的**规则文件**。新增或修改 `cmd/stella/` 下的命令前，先读这页并遵守它。Stella 遵循 [Command Line Interface Guidelines](https://github.com/cli-guidelines/cli-guidelines) 的精神：命令要可预测、可脚本化、可组合，并且善待凌晨两点还在终端里的用户。

Stella CLI 是 `stellad server` 的一等人类/operator 客户端，不是第二套后端，也不是 agent 集成界面。设计会修改服务端状态的功能前，先读 [CLI 与原生 agent 工具](../cli-as-client) 和 [API design rules](./api-design)。

## 核心原则

1. **默认照顾人，需要时照顾机器。** 默认输出应适合人在终端阅读；结构化输出放在 `--json` 后。
2. **无聊胜过聪明。** 命令应该只做名字暗示的事。避免隐藏行为、隐式网络写入和意外默认值。
3. **可组合很重要。** 命令要能进脚本：稳定退出码、诊断走 stderr、数据走 stdout、非交互场景不要突然提问。
4. **一个命令做一件事。** 子命令要目的明确。如果需要几段话解释它做什么，拆开或改名。
5. **保护用户数据。** 破坏性命令需要明确意图和清晰错误。不要静默删除、覆盖或撤销。

## 命令形状

使用项目约定：

```text
stella <noun> <verb> [args] [flags]
```

示例：

```text
stella recally save <url>
stella recally feed add <url>
stella task cancel <task-id>
stella share artifact <path>
```

### 命名

- 顶层命令是领域名词：`recally`、`task`、`skill`、`vault`。
- 子命令是动词；需要再分层时可以用资源名词：`list`、`get`、`create`、`cancel`、`feed add`。
- 多词命令和 flag 使用小写 kebab-case。
- 常见动作保持一致：

| 动作             | 使用                             | 避免                                    |
| ---------------- | -------------------------------- | --------------------------------------- |
| 展示多个资源     | `list`                           | `ls`、`all`、`show-all`                 |
| 展示单个资源     | `get` 或 `read`                  | 除非已有惯例，否则别混用 `show`、`info` |
| 创建资源         | `create` 或领域化的 `add`/`save` | 随机混用 `new` 和 `create`              |
| 修改资源         | `update`                         | `modify`，除非是交互编辑才用 `edit`     |
| 移除资源         | `delete` 或领域化的 `remove`     | `destroy`、`rm`                         |
| 停止运行中的工作 | `cancel`                         | `kill`、`abort`                         |

保持现有命令名稳定。如果值得重命名，除非旧行为本身不安全，否则至少保留旧名作为一个版本的 alias。

### 参数 vs flags

位置参数用于命令操作的主要对象：

```text
stella task get <task-id>
stella share artifact <path>
```

flags 用于修饰项、可选上下文、过滤和输出控制：

```text
stella task list --status open --json
```

规则：

- 必填位置参数必须写进 `ArgsUsage`。
- 只有没有自然的位置参数形式时，才使用必填 flag。
- 布尔 flag 应使用正向语义：`--follow`、`--json`、`--force`。除非功能就是关闭默认行为，否则避免 `--no-cache` 这类反向 flag。
- 跨命令复用 flag 名：`--server-url`、`--json`、`--force`、`--limit`。
- 不要设计面向 sandbox 的 CLI 命令。agent 能力必须作为带服务端身份的原生工具交付，而不是 CLI flag 或 scoped bearer token。

## 帮助文本

用户应该能从 `stella help <command>` 理解每个命令。

按 `urfave/cli` 的字段：

- `Name`：短小的小写命令 token。
- `Usage`：一句话，不加句号。
- `Description`：只有命令需要背景、示例或警告时才写。
- `ArgsUsage`：列出所有位置参数，例如 `<task-id>`。
- `Category`：顶层命令需要改善主帮助页时设置（`Feature`、`System`、`Admin`）。

好：

```go
Usage:    "Create public share links",
ArgsUsage: "<path>",
```

差：

```go
Usage: "Does stuff with the thing",
```

帮助文本是用户文档。命令名、usage 字符串或重要 flag 改变时，更新用户文档。面向 agent 的 prompt 和 skill 应指向原生工具，而不是 CLI 命令语法。

## 输出

### stdout 放数据

任何可能被用户 pipe 给其他程序的内容都走 stdout：

```bash
stella task list --json | jq '.tasks[].id'
```

人类可读表格和成功摘要也可以走 stdout。它们要足够稳定，别惩罚轻量脚本用户；但表格格式不是 API。

### stderr 放诊断

进度、警告、提示和错误走 stderr。这样 stdout 才能干净地用于 pipe 和命令替换。

```text
Downloading attachments...        # stderr
{"id":"...","status":"done"}    # stdout
```

### JSON 输出

预期用于脚本的命令应支持 `--json`，除非它唯一输出的本来就是原始标量或文件内容。

规则：

- stdout 只输出合法 JSON，不夹杂其他内容。
- 字段名沿用 API 的 `snake_case`。
- 优先直接使用 API 响应形状；不要发明第二套 CLI schema。
- 为人类阅读做 pretty-print 可以，但结构不要变。
- 错误仍然走 stderr 并返回非零退出码；失败时不要打印成功 JSON envelope。

### 表格

人类列表输出使用对齐列和简短表头。避免在表格里塞长文本；长内容放进 `read`、`get` 或 `--json` 输出。

尽量把稳定 ID 放第一列：

```text
ID        STATUS    TITLE
abc123    open      Fix scheduler retry
```

## 错误和退出码

错误要简短、具体、可行动：

```text
missing bearer token: set STELLA_TOKEN or sign in through the Web UI
```

差的错误会泄漏实现细节或让用户猜：

```text
sql: no rows in result set
invalid input
```

退出码规则：

| 代码 | 含义                                             |
| ---- | ------------------------------------------------ |
| 0    | 成功                                             |
| 1    | 预期失败：校验失败、未找到、服务端错误、认证失败 |
| 2    | CLI 框架可区分时的命令行用法错误                 |

除非真实自动化场景需要，不要引入复杂退出码分类。多数时候，一位 failure bit 就够。

Go 里包装错误时，只加一次操作上下文：

```go
return fmt.Errorf("task cancel: %w", err)
```

不要每层都包同一个名词，最后错误信息像闹鬼的 stack trace。

## 交互

只有 stdin 是终端、并且命令明显面向用户交互时，才允许交互行为。

规则：

- 提问前先检测非交互场景。
- 给脚本提供 flag，不要强迫 prompt。
- 破坏性操作的确认提示要展示会改变什么。
- `--force` 可以跳过确认，但不能扩大操作范围。
- 如果值能从 vault 或标准环境变量读取，不要提示用户输入 secret。

## 破坏性命令

会删除、撤销、覆盖、取消、归档或发送外部可见内容的命令，都是破坏性命令。

要求：

1. 命令名必须让动作明显：`delete`、`remove`、`cancel`、`revoke`、`send`。
2. 目标选择必须明确。避免对隐式“当前”资源执行破坏性操作。
3. 批量破坏性操作需要窄过滤条件加确认，或显式 `--force`。
4. 宽范围操作优先支持 dry-run。

不要让 `stella <thing> sync` 删除远端数据，除非帮助文本和确认提示都明白写出来。意外删除就是工具被卸载的最快路径，毫无悬念。

## 配置和环境变量

配置优先级保持一致：

```text
flag > environment variable > persisted config > default
```

环境变量影响行为时，要在帮助文本里说明。常见变量：

| 变量                | 用途                                                  |
| ------------------- | ----------------------------------------------------- |
| `STELLA_SERVER_URL` | CLI-as-client 命令的服务端 base URL                   |
| `STELLA_TOKEN`      | 显式提供时供人类/operator CLI 请求使用的 bearer token |
| `LOG_LEVEL`         | CLI 日志级别                                          |

永远不要打印 secret。命令必须展示 secret 存在时，只展示元数据或脱敏值。

## 网络和服务端访问

服务端状态支撑的功能，CLI 应调用生成的 API client，不要打开数据库、读取服务端拥有的文件或复制业务逻辑。

模式：

1. 从 args 和 flags 构造 typed request。
2. 调用 `apiclient.Call` / 生成的 client。
3. 渲染响应。
4. 返回带命令上下文的错误。

服务端不可用时，告诉用户怎么修：

```text
connect to Stella server: connection refused (start it with `stellad server` or set STELLA_SERVER_URL)
```

## 日志和详细程度

- 正常成功的命令应该安静。
- 只有操作耗时明显时，才向 stderr 打进度。
- Debug 日志由 `LOG_LEVEL` 控制，绝不能污染 stdout。
- 不要记录包含 secret、token、邮件内容或用户 prompt 的请求体，除非已经明确脱敏。

## 兼容性

CLI 用户会把一切写进脚本。命令名、flag 名、JSON 字段和退出行为都是兼容性表面。

- 新增 flag，而不是改变现有 flag 含义。
- 重命名命令时保留旧 alias。
- 如果用户可能 pipe 默认输出，避免改变默认输出顺序。
- JSON 字段优先做加法；不要改变字段类型。
- 删除行为前先检查文档、测试和已知调用方。

**Pre-launch exception（发布前例外）。** Stella 尚未发布稳定版本，因此没有需要保护
的外部脚本。在首次发布之前，优先完整遵循规范而非保留兼容垫片：直接把命令重命名为正确
的形态，并彻底移除遗留 alias（包括 `rm` 之类的破坏性 alias），而不是继续保留它们。
发布之后，上述兼容性规则才完整生效。

**`vault get <name>` 密钥例外。** `vault get <name>` 会刻意把原始密钥值打印到 stdout
——它是用于脚本的显式单资源读取，是"绝不打印密钥"规则唯一被认可的例外。其他所有表面
（例如 `vault list`、`email config get/list`）都必须在人类可读和 JSON 输出中对密钥做
脱敏。

## 实现检查清单

新增或修改 `cmd/stella/` 命令时：

1. 命令是否符合 `stella <noun> <verb>` 和现有领域命名？
2. 主要目标是否用位置参数，修饰项是否用 flags？
3. `Usage`、`Description`、`ArgsUsage` 在 `stella help` 里是否清楚？
4. stdout 是否只包含命令数据，诊断是否走 stderr？
5. 适合脚本的输出是否支持 `--json`？
6. 错误是否可行动，并带有有用的命令上下文？
7. 破坏性动作是否明确，并避免意外扩大影响？
8. 配置优先级是否遵循 `flag > env > config > default`？
9. 对服务端状态，命令是否使用生成的 API client，而不是直接写数据库或文件？
10. 如果 command usage 改了，文档和 `internal/agent/prompt/template/system_prompt.tmpl` 是否同步？
11. 是否更新了 args、flags、输出和错误行为相关的命令测试？
