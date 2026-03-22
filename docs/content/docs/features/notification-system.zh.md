---
title: 通知系统
---

## 状态

已实现——`internal/channel/notifier.go`, `internal/channel/notify_tool.go`, `internal/channel/telegram/telegram.go`。

## 概述

Anna 支持主动通知,因此代理、定时任务和其他内部触发器可以在不等待请求的情况下向用户推送消息。该系统使用多通道分发器将通知路由到一个或多个配置的通道(Telegram、QQ、微信,计划支持 Slack/Discord)。

## 架构

```
+-------------------+
|  Agent (notify     |--+
|  tool call)        |  |
+-------------------+  |
                        |   Notification{Channel, ChatID, Text, Silent}
+-------------------+  |           |
|  Scheduler job result |--+----------v------------------+
+-------------------+  |      Dispatcher              |
                        |  +----------------------+   |
+-------------------+  |  | Route by Channel     |   |
|  Future triggers   |--+  | or broadcast all     |   |
+-------------------+     +----------+-----------+   |
                                     |               |
                        +------------+----------+    |
                        |            |          |    |
                        v            v          v    |
                  +----------+ +--------+ +-------+  |
                  | Telegram | | Slack  | |Discord|  |
                  | Channel  | |(future)| |(future)| |
                  +----------+ +--------+ +-------+  |
```

## 关键类型

### `channel.Notification`

```go
type Notification struct {
    Channel string // 可选: 路由到特定后端("telegram", "slack")
    ChatID  string // 后端内的目标聊天/频道
    Text    string // markdown 内容
    Silent  bool   // 发送时不发出通知声音
}
```

- `Channel` 为空——广播到**所有**已注册通道
- `Channel` 已设置——仅路由到该特定通道
- `ChatID` 为空——每个通道使用其配置的默认值

### `channel.Channel`

所有消息平台实现的接口:

```go
type Channel interface {
    Name() string
    Start(ctx context.Context) error
    Stop()
    Notify(ctx context.Context, n Notification) error
}
```

当前已实现: `telegram.Bot`, `qq.Bot`, `weixin.Bot`。

### `channel.Dispatcher`

将通知路由到已注册通道:

```go
d := channel.NewDispatcher()
d.Register(tgBot, "136345060")   // telegram 通道,带默认聊天
d.Register(qqBot, "")            // qq 通道

// 广播到所有通道(每个使用其默认聊天):
d.Notify(ctx, channel.Notification{Text: "hello"})

// 路由到特定通道:
d.Notify(ctx, channel.Notification{Channel: "telegram", Text: "hello"})

// 覆盖默认聊天:
d.Notify(ctx, channel.Notification{Channel: "telegram", ChatID: "999", Text: "hello"})
```

部分失败: 如果在广播期间一个通道失败,其他通道仍会接收通知。错误通过 `errors.Join` 合并。

### `channel.NotifyTool`

包装分发器的面向代理的工具:

```go
tool := channel.NewNotifyTool(dispatcher)
```

LLM 可以通过以下方式调用它:

```json
{
  "message": "Build finished, 3 tests failed",
  "channel": "telegram",
  "chat_id": "136345060",
  "silent": false
}
```

- `message` (必需)——通知文本
- `channel` (可选)——定位特定通道;省略以广播
- `chat_id` (可选)——覆盖通道的默认目标
- `silent` (可选)——抑制通知声音

## 连接

### 启动流程 (`main.go`)

```
setup()
  +-- Create Dispatcher
  +-- Create NotifyTool(dispatcher) -> extraTools
  +-- Create runner factory with extraTools
  +-- Create PoolManager

runGateway()
  +-- Create telegram.Bot
  +-- dispatcher.Register(tgBot, notifyChat)  <- 通道已注册
  +-- wireSchedulerNotifier(schedulerSvc, poolManager, dispatcher) <- 调度器输出 -> 分发器
  +-- tgBot.Start(ctx)                        <- 开始轮询
```

分发器在早期(在 `setup` 中)创建,因此通知工具可以引用它。通道稍后(在 `runGateway` 中)创建时注册。这避免了循环依赖。`wireSchedulerNotifier` 函数通过 PoolManager 而不是单个池路由。

### Cron 到通知

当定时任务触发时:

1. 任务通过 `PoolManager.Chat()` 运行,使用任务的 `agent_id` 和 `user_id` 到达正确的代理
2. 收集完整的响应文本
3. 文本通过 `dispatcher.Notify()` 广播到所有通道

### CLI 模式

在 CLI 模式(`anna chat`)中,没有注册通知通道,因此 `notify` 工具不会暴露给代理。这避免了损坏的工具路径。

## 配置

通道配置通过管理面板管理。每个通道的设置(令牌、聊天 ID、群组模式、允许的 ID)作为 JSON 存储在数据库中。从管理面板 UI 配置通知通道,而不是直接编辑配置文件。

### 通知目标解析

当调用 `Notify()` 时,目标聊天按以下顺序解析:

1. `Notification.ChatID`(调用中的显式值)
2. 通道注册的默认聊天(来自 `dispatcher.Register`)
3. 对于 Telegram: `notify_chat` -> `channel_id` -> 错误

## 添加新通道

要添加 Slack、Discord 或任何其他通道:

1. **实现 `channel.Channel`:**

```go
// channel/slack/slack.go
type Bot struct { ... }

func (b *Bot) Name() string                                          { return "slack" }
func (b *Bot) Start(ctx context.Context) error                       { /* start listening */ }
func (b *Bot) Stop()                                                 { /* graceful shutdown */ }
func (b *Bot) Notify(ctx context.Context, n channel.Notification) error {
    // 通过 Slack API 将 n.Text 发送到 n.ChatID
}
```

使用 `channel.NewCommander(pool, listFn, switchFn)` 处理共享的 `/new`, `/compact`, `/model` 命令逻辑。`/whoami` 按通道处理,因为每个平台返回不同的 ID 格式。使用 `channel.SplitMessage()` 和 `channel.FormatDuration()` 处理共享实用程序。

2. **在 `runGateway()` 中注册:**

```go
if slackCfg.Token != "" {
    slackBot := slack.New(slackCfg)
    channels = append(channels, slackBot)
    s.notifier.Register(slackBot, slackCfg.NotifyChannel)
}
```

3. **通过管理面板添加通道配置**。通道配置作为 JSON 存储在数据库 settings 表中。

不需要更改分发器、通知工具或调度器连接——它们通过 `Channel` 接口工作。

## Telegram 特定功能

### 群组支持

机器人可以在 Telegram 群组中运行,具有可配置的行为:

- `mention` (默认)——仅在 @ 提及或回复时响应
- `always`——响应群组中的每条消息
- `disabled`——忽略所有群组消息(包括命令)

群组的会话 ID = 群组聊天 ID(每个群组的共享上下文)。

### 访问控制

`allowed_ids` 限制机器人交互到特定用户 ID(Telegram 数字 ID、QQ OpenID、Feishu open_id、微信 iLink 用户 ID)。当列表为空时,允许所有用户。未授权用户会被静默忽略——所有处理器(命令、回调、文本)都包装在访问检查中。用户可以向机器人发送 `/whoami` 来发现他们的 ID。

### 通知传递

`telegram.Bot.Notify()` 支持:

- 数字聊天 ID(`"136345060"`)
- 频道用户名(`"@my_channel"`)
- Markdown 渲染,MarkdownV2 回退到纯文本
- 在 4000 字符边界处拆分消息
- 静默模式(`DisableNotification`)
