---
title: 通知システム
---

## ステータス

実装済み -- `internal/channel/notifier.go`, `internal/channel/notify_tool.go`, `internal/channel/telegram/telegram.go`。

## 概要

Annaは、エージェント、スケジュールされたジョブ、その他の内部トリガーがリクエストを待たずにユーザーにメッセージをプッシュできるように、プロアクティブな通知をサポートしています。このシステムは、1つ以上の設定されたチャネル（Telegram、QQ、WeChat、SlackやDiscordも計画中）に通知をルーティングするマルチチャネルディスパッチャーを使用します。

## アーキテクチャ

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

## 主要な型

### `channel.Notification`

```go
type Notification struct {
    Channel string // オプション: 特定のバックエンドにルーティング（"telegram", "slack"）
    ChatID  string // バックエンド内のターゲットチャット/チャネル
    Text    string // markdownコンテンツ
    Silent  bool   // 通知音なしで送信
}
```

- `Channel`が空 -- **すべて**の登録されたチャネルにブロードキャスト
- `Channel`が設定 -- その特定のチャネルのみにルーティング
- `ChatID`が空 -- 各チャネルは設定されたデフォルトを使用

### `channel.Channel`

すべてのメッセージングプラットフォームが実装するインターフェース:

```go
type Channel interface {
    Name() string
    Start(ctx context.Context) error
    Stop()
    Notify(ctx context.Context, n Notification) error
}
```

現在実装済み: `telegram.Bot`, `qq.Bot`, `weixin.Bot`。

### `channel.Dispatcher`

登録されたチャネルに通知をルーティングします。

```go
d := channel.NewDispatcher()
d.Register(tgBot)                // telegramチャネル
d.Register(qqBot)                // qqチャネル

// すべてのチャネルにブロードキャスト（各チャネルはデフォルトチャットを使用）:
d.Notify(ctx, channel.Notification{Text: "hello"})

// 特定のチャネルにルーティング:
d.Notify(ctx, channel.Notification{Channel: "telegram", Text: "hello"})

// デフォルトチャットをオーバーライド:
d.Notify(ctx, channel.Notification{Channel: "telegram", ChatID: "999", Text: "hello"})
```

部分的な失敗: ブロードキャスト中に1つのチャネルが失敗しても、他のチャネルは通知を受信します。エラーは`errors.Join`経由で結合されます。

### `channel.NotifyTool`

ディスパッチャーをラップするエージェント向けツール:

```go
tool := channel.NewNotifyTool(dispatcher)
```

LLMは次のように呼び出すことができます。

```json
{
  "message": "Build finished, 3 tests failed",
  "channel": "telegram",
  "chat_id": "136345060",
  "silent": false
}
```

- `message`（必須）-- 通知テキスト
- `channel`（オプション）-- 特定のチャネルをターゲット; 省略するとブロードキャスト
- `chat_id`（オプション）-- チャネルのデフォルトターゲットをオーバーライド
- `silent`（オプション）-- 通知音を抑制

## 配線

### 起動フロー（`main.go`）

```
setup()
  +-- Create Dispatcher
  +-- Create NotifyTool(dispatcher) -> builtinTools
  +-- Create runner factory with builtinTools
  +-- Create PoolManager

runGateway()
  +-- Create telegram.Bot
  +-- dispatcher.Register(tgBot)               <- チャネル登録
  +-- wireSchedulerNotifier(schedulerSvc, poolManager, dispatcher) <- スケジューラー出力 -> ディスパッチャー
  +-- tgBot.Start(ctx)                        <- ポーリング開始
```

ディスパッチャーは早期に（`setup`で）作成されるため、notifyツールがそれを参照できます。チャネルは後で（`runGateway`で）作成時に登録されます。これにより循環依存が回避されます。`wireSchedulerNotifier`関数は単一プールではなくPoolManager経由でルーティングします。

### Cronから通知へ

スケジュールされたジョブが起動すると:

1. ジョブはジョブの`agent_id`と`user_id`を使用して`PoolManager.Chat()`経由で実行され、正しいエージェントに到達します
2. 完全なレスポンステキストが収集されます
3. テキストは`dispatcher.Notify()`経由ですべてのチャネルにブロードキャストされます

### CLIモード

CLIモード（`anna chat`）では、通知チャネルは登録されないため、`notify`ツールはエージェントに公開されません。これにより壊れたツールパスが回避されます。

## 設定

チャネル設定は管理パネルで管理されます。各チャネルの設定（トークン、チャットID、グループモード）は、データベースにJSONとして保存されます。設定ファイルを直接編集するのではなく、管理パネルUIから通知チャネルを設定してください。

### 通知ターゲット解決

`Notify()`が呼び出されると、ターゲットチャットは次の順序で解決されます。

1. `Notification.ChatID`（呼び出しで明示的）
2. チャネルのデフォルトターゲット（チャネル設定から）
3. Telegramの場合: `channel_id` -> エラー

## 新しいチャネルの追加

Slack、Discord、またはその他のチャネルを追加するには:

1. **`channel.Channel`を実装:**

```go
// channel/slack/slack.go
type Bot struct { ... }

func (b *Bot) Name() string                                          { return "slack" }
func (b *Bot) Start(ctx context.Context) error                       { /* start listening */ }
func (b *Bot) Stop()                                                 { /* graceful shutdown */ }
func (b *Bot) Notify(ctx context.Context, n channel.Notification) error {
    // Slack API経由でn.TextをN.ChatIDに送信
}
```

共有の`/new`、`/compact`、`/model`コマンドロジックには`channel.NewCommander(pool, listFn, switchFn)`を使用します。`/whoami`は、各プラットフォームが異なるID形式を返すため、チャネルごとに処理されます。共有ユーティリティには`channel.SplitMessage()`と`channel.FormatDuration()`を使用します。

2. **`runGateway()`で登録:**

```go
if slackCfg.Token != "" {
    slackBot := slack.New(slackCfg)
    channels = append(channels, slackBot)
    s.notifier.Register(slackBot)
}
```

3. **チャネル設定を追加**（管理パネル経由）。チャネル設定はデータベースのsettingsテーブルにJSONとして保存されます。

ディスパッチャー、notifyツール、またはスケジューラー配線への変更は不要です。これらは`Channel`インターフェースを介して動作します。

## Telegram固有の機能

### グループサポート

ボットは設定可能な動作でTelegramグループで動作できます。

- `mention`（デフォルト）-- @メンションされたか返信された場合のみ応答
- `always` -- グループ内のすべてのメッセージに応答
- `disabled` -- すべてのグループメッセージを無視（コマンドを含む）

グループのセッションID = グループチャットID（グループごとに共有コンテキスト）。

### アクセス制御

アクセス制御はRBACシステムで管理されます。ユーザーはプラットフォームID（Telegram数値ID、QQ OpenID、Feishu open_id、WeChat iLinkユーザーID）を通じて認証システムに自動的に関連付けられます。権限のないユーザーは静かに無視されます。すべてのハンドラー（コマンド、コールバック、テキスト）はアクセスチェックでラップされます。ユーザーは自分のIDを確認するために`/whoami`をボットに送信できます。

### 通知配信

`telegram.Bot.Notify()`は以下をサポートします。

- 数値チャットID（`"136345060"`）
- チャネルユーザー名（`"@my_channel"`）
- プレーンテキストへのMarkdownV2フォールバックでのMarkdownレンダリング
- 4000文字境界でのメッセージ分割
- サイレントモード（`DisableNotification`）
