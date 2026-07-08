---
title: 快速开始
---

五分钟内启动 Stella，开始你的第一次 AI 对话。

## 安装

**Homebrew（macOS 和 Linux）：**

```bash
brew install CherryHQ/tap/stella
```

**Go install：**

```bash
go install github.com/CherryHQ/stella/cmd/stellad@latest
```

**下载二进制文件：**

从 [Releases](https://github.com/CherryHQ/stella/releases) 下载最新版本，或对已有安装执行自动更新：

```bash
stellad upgrade
```

## 设置 API 密钥

你需要至少一个服务商的 API 密钥。Stella 支持 Anthropic、OpenAI 以及任何 OpenAI 兼容的 API。

将密钥设为环境变量，Stella 启动时会自动读取：

```bash
# 选择一个（或两个都设置）
export ANTHROPIC_API_KEY="sk-ant-..."
export OPENAI_API_KEY="sk-..."
```

你也可以将这些添加到 shell 配置文件（`~/.zshrc`、`~/.bashrc` 等），这样它们会在每次打开终端时自动生效。

## 启动服务

```bash
stellad server
```

Stella 启动后会在 [http://localhost:25678](http://localhost:25678) 提供Web UI。在浏览器中打开它。

如需使用其他端口：

```bash
stellad server --port 8080
```

## 配置服务商

1. 打开Web UI [http://localhost:25678](http://localhost:25678)。
2. 点击侧边栏的 **Providers**。
3. 点击 **Add Provider** 并输入你的 API 密钥。
4. Stella 会自动检测你的服务商提供的可用模型。

如果你在上一步已经设置了环境变量，Stella 会自动将其作为备选方案使用。在Web UI中配置服务商可以让你更精确地控制使用哪些模型。

## 开始你的第一次对话

你有两种方式：

**从Web UI：** 打开 **Chat** 部分，直接开始输入。Stella 会使用你配置的模型进行回复。

**从 Telegram：** 连接一个 Telegram 机器人，从手机上与 Stella 聊天。设置说明请参阅 [Telegram 频道指南](/docs/channels/telegram)。

## 下一步

- [将 Stella 部署为服务](/docs/start-here/deployment)，使其在开机时自动运行
- 在Web UI中[配置智能体、模型和设置](/docs/start-here/configuration)
- 连接 [Telegram](/docs/channels/telegram)、[QQ](/docs/channels/qq)、[飞书](/docs/channels/feishu) 或[微信](/docs/channels/weixin)，随时随地聊天
- [设置提醒和定时任务](/docs/guides/scheduling)，让 Stella 自动工作
- [浏览并安装技能](/docs/guides/skills)，扩展 Stella 的能力
- [查阅 API 文档](/api-references)，了解完整的 REST API 接口
