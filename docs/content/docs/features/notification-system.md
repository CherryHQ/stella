---
title: Notification System
---

## Status

Implemented -- `internal/channel/notifier.go`, `internal/channel/notify_tool.go`, `internal/channel/telegram/telegram.go`.

## Overview

Anna supports proactive notifications so the agent, scheduled jobs, and other internal triggers can push messages to users without waiting for a request. The system uses a multi-channel dispatcher that routes notifications to one or more configured channels (Telegram, QQ, with Slack/Discord planned).

## Architecture

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

## Key Types

### `channel.Notification`

```go
type Notification struct {
    Channel string // optional: route to specific backend ("telegram", "slack")
    ChatID  string // target chat/channel within the backend
    Text    string // markdown content
    Silent  bool   // send without notification sound
}
```

- `Channel` empty -- broadcast to **all** registered channels
- `Channel` set -- route to that specific channel only
- `ChatID` empty -- each channel uses its configured default

### `channel.Channel`

Interface that all messaging platforms implement:

```go
type Channel interface {
    Name() string
    Start(ctx context.Context) error
    Stop()
    Notify(ctx context.Context, n Notification) error
}
```

Currently implemented: `telegram.Bot`, `qq.Bot`.

### `channel.Dispatcher`

Routes notifications to registered channels:

```go
d := channel.NewDispatcher()
d.Register(tgBot, "136345060")   // telegram channel with default chat
d.Register(qqBot, "")            // qq channel

// Broadcast to all channels (each uses its default chat):
d.Notify(ctx, channel.Notification{Text: "hello"})

// Route to specific channel:
d.Notify(ctx, channel.Notification{Channel: "telegram", Text: "hello"})

// Override the default chat:
d.Notify(ctx, channel.Notification{Channel: "telegram", ChatID: "999", Text: "hello"})
```

Partial failures: if one channel fails during broadcast, the others still receive the notification. Errors are joined via `errors.Join`.

### `channel.NotifyTool`

Agent-facing tool that wraps the dispatcher:

```go
tool := channel.NewNotifyTool(dispatcher)
```

The LLM can call it with:

```json
{
  "message": "Build finished, 3 tests failed",
  "channel": "telegram",
  "chat_id": "136345060",
  "silent": false
}
```

- `message` (required) -- the notification text
- `channel` (optional) -- target a specific channel; omit to broadcast
- `chat_id` (optional) -- override the channel's default target
- `silent` (optional) -- suppress notification sound

## Wiring

### Startup Flow (`main.go`)

```
setup()
  +-- Create Dispatcher
  +-- Create NotifyTool(dispatcher) -> extraTools
  +-- Create runner factory with extraTools
  +-- Create PoolManager

runGateway()
  +-- Create telegram.Bot
  +-- dispatcher.Register(tgBot, notifyChat)  <- channel registered
  +-- wireSchedulerNotifier(schedulerSvc, poolManager, dispatcher) <- scheduler output -> dispatcher
  +-- tgBot.Start(ctx)                        <- begin polling
```

The dispatcher is created early (in `setup`) so the notify tool can reference it. Channels are registered later (in `runGateway`) when they are created. This avoids circular dependencies. The `wireSchedulerNotifier` function routes through PoolManager rather than a single pool.

### Cron to Notification

When a scheduled job fires:

1. The job runs through `PoolManager.Chat()` using the job's `agent_id` and `user_id` to reach the correct agent
2. The full response text is collected
3. The text is broadcast via `dispatcher.Notify()` to all channels

### CLI Mode

In CLI mode (`anna chat`), no notification channels are registered, so the `notify` tool is not exposed to the agent. This avoids a broken tool path.

## Configuration

Channel configuration is managed through the admin panel. Each channel's settings (tokens, chat IDs, group modes, allowed IDs) are stored as JSON in the database. Configure notification channels from the admin panel UI rather than editing configuration files directly.

### Notify Target Resolution

When `Notify()` is called, the target chat is resolved in this order:

1. `Notification.ChatID` (explicit in the call)
2. Channel's registered default chat (from `dispatcher.Register`)
3. For Telegram: `notify_chat` -> `channel_id` -> error

## Adding a New Channel

To add Slack, Discord, or any other channel:

1. **Implement `channel.Channel`:**

```go
// channel/slack/slack.go
type Bot struct { ... }

func (b *Bot) Name() string                                          { return "slack" }
func (b *Bot) Start(ctx context.Context) error                       { /* start listening */ }
func (b *Bot) Stop()                                                 { /* graceful shutdown */ }
func (b *Bot) Notify(ctx context.Context, n channel.Notification) error {
    // Send n.Text to n.ChatID via Slack API
}
```

Use `channel.NewCommander(pool, listFn, switchFn)` for shared `/new`, `/compact`, `/model` command logic. `/whoami` is handled per-channel since each platform returns different ID formats. Use `channel.SplitMessage()` and `channel.FormatDuration()` for shared utilities.

2. **Register in `runGateway()`:**

```go
if slackCfg.Token != "" {
    slackBot := slack.New(slackCfg)
    channels = append(channels, slackBot)
    s.notifier.Register(slackBot, slackCfg.NotifyChannel)
}
```

3. **Add channel config** via the admin panel. Channel configuration is stored as JSON in the database settings table.

No changes needed to the dispatcher, notify tool, or scheduler wiring -- they work through the `Channel` interface.

## Telegram-Specific Features

### Group Support

The bot can operate in Telegram groups with configurable behavior:

- `mention` (default) -- respond only when @mentioned or replied to
- `always` -- respond to every message in the group
- `disabled` -- ignore all group messages (including commands)

Session ID for groups = group chat ID (shared context per group).

### Access Control

`allowed_ids` restricts bot interaction to specific user IDs (Telegram numeric IDs, QQ OpenIDs, Feishu open_ids). When the list is empty, all users are allowed. Unauthorized users are silently ignored -- all handlers (commands, callbacks, text) are wrapped in the access check. Users can send `/whoami` to the bot to discover their ID.

### Notification Delivery

`telegram.Bot.Notify()` supports:

- Numeric chat IDs (`"136345060"`)
- Channel usernames (`"@my_channel"`)
- Markdown rendering with MarkdownV2 fallback to plain text
- Message splitting at 4000-char boundaries
- Silent mode (`DisableNotification`)
