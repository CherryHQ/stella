---
title: 密钥管理
---

## 保险库的作用

Stella 的保险库会加密存储 API key、令牌和其他机密信息。用户密钥只有三个部分：**名称、值、作用域**。

投递是自动的。Agent 会话启动时，Stella 会把所有匹配作用域的用户密钥放进 sandbox 环境变量，环境变量名就是密钥名。如果某个 CLI 需要 `GITHUB_TOKEN`，就把密钥命名为 `GITHUB_TOKEN`。

OAuth 凭据等系统托管密钥只供 Stella 内部代码读取，不会暴露成 sandbox 环境变量。

## 设置

使用保险库前，先生成主加密密钥并提供给 Stella。

### 1. 生成主密钥

```bash
stellad vault keygen
```

复制以 `AGE-SECRET-KEY-1` 开头的那一行。`stellad vault keygen --help` 是引导参数的权威来源。

### 2. 使用密钥启动 Stella

```bash
export STELLA_VAULT_KEY="AGE-SECRET-KEY-1..."
stellad server
```

> **请备份主密钥。** 如果丢失，所有已存储密钥都会永久无法恢复。请把它保存在密码管理器或其他安全位置。

这就是全部设置。新用户加入时，Stella 会自动处理每个用户的加密。

## 添加密钥

### 从 Web UI

1. 打开 Web UI，进入 Credentials 页面。
2. 在 Vault 区域选择密钥作用域。
3. 输入密钥名称，必须和工具期望的环境变量名完全一致。
4. 输入值并保存。

密钥值在 Web UI 中是只写的。之后你可以查看名称和元数据，但不能显示明文值。

### 密钥作用域

| 作用域                  | 谁可以设置 | 会作为环境变量出现在  |
| ----------------------- | ---------- | --------------------- |
| 我的凭据 · 所有 Agent   | 你         | 你的所有 Agent 会话   |
| 我的凭据 · 指定 Agent   | 你         | 所选 Agent 的会话     |
| 管理员凭据 · 全局       | 管理员     | 所有用户的 Agent 会话 |
| 管理员凭据 · 指定 Agent | 管理员     | 所选 Agent 的会话     |

多个作用域定义同名密钥时，Stella 按这个优先级取值：你的指定 Agent 密钥、你的所有 Agent 密钥、管理员指定 Agent 密钥、管理员全局密钥。

### 从聊天会话

你也可以在对话中存储密钥。当 Stella 需要一个 key 时，用 config 命令发送：

```text
/config SECRET_NAME your-secret-value
```

这会把值直接写入保险库，不会暴露在对话历史中。密钥存储后，Stella 会继续你的任务。

你也可以让 Stella 管理密钥：

- **“列出我的保险库密钥。”**
- **“删除 GITHUB_TOKEN 密钥。”**

## 在会话中使用密钥

匹配作用域的密钥会自动出现在 sandbox 会话里。名为 `GITHUB_TOKEN` 的密钥会以 `$GITHUB_TOKEN` 的形式供 bash 命令和第三方 CLI 使用。

你不需要再把密钥单独绑定到 Agent 或项目。作用域就是唯一的投递控制。

群组会话不会收到 vault 密钥。

每次会话都会重新加载密钥。如果你添加或更新了密钥，更改会在下次会话生效。

Agent 会话不会收到 Stella API bearer token。Agent 使用内置工具访问 Stella 能力，而不是在 sandbox 内调用 HTTP API。

## 密钥命名规则

密钥名称必须遵循以下规则：

- 仅限大写字母、数字和下划线，例如 `MY_API_KEY`
- 必须以字母开头
- 最长 128 个字符
- 不能使用系统托管凭据名称，例如 `STELLA_TOKEN`、`GH_OAUTH`、`LARK_CLI_OAUTH` 或 `FEISHU_CLI_OAUTH`
- 不能以 `STELLA_`、`OAUTH_`、`MCP_TOKEN_`、`LD_` 或 `DYLD_` 等保留前缀开头
- 不能使用执行钩子名称，例如 `BASH_ENV`、`ENV`、`PROMPT_COMMAND`、`GIT_SSH_COMMAND`、`NODE_OPTIONS` 或 `PYTHONSTARTUP`

## 小贴士

- **按工具的环境变量命名。** 如果工具文档写的是 `OPENAI_API_KEY`，就使用这个确切名称。
- **尽量缩小作用域。** 只给真正需要该服务 key 的 Agent 配置密钥。
- **密钥在重启后仍然存在。** 只需要设置一次。
- **通过覆盖来轮换。** 用相同名称和作用域保存新值即可替换旧值。
