---
title: アーキテクチャ
---

## システム概要

annaは、`main.go`で結び付けられた疎結合なパッケージのセットとして構成されています。システムは複数のユーザーと複数のエージェントをサポートし、ルーティングはメッセージごとに処理されます。コアフローは以下の通りです：

1. **チャネル**（CLI、Telegram、QQ、Feishu、またはWeChat）がユーザー入力を受信
2. チャネルは**ユーザーを解決**（外部ID + プラットフォームでアップサート）し、**エージェントを解決**（DMデフォルト、グループバインディング、またはフォールバック）
3. **PoolManager**がエージェントIDでエージェントの**Pool**を検索（または作成）
4. **Pool**がセッションを管理し、**Runner**にディスパッチ
5. **Go runner**が`internal/ai/`経由でLLMプロバイダーを呼び出し、ループ内でツールを実行
6. レスポンスがチャネル経由でユーザーにストリーミングバック

```
Channel (CLI / Telegram / QQ / Feishu / WeChat)
    |
    v
Resolve user (identity.go)  -->  Resolve agent (identity.go)
    |
    v
 PoolManager.Get(agentID)  -->  Pool (sessions + runner lifecycle)
    |
    v
Go Runner (agent loop + tools)
    |
    v
LLM Provider (Anthropic / OpenAI / OpenAI-compatible)
```

セッションキーはエージェントごとにスコープされます：`{agentID}:{platform}:{userID}:{context}`。これにより、同じユーザーが異なるエージェントと会話した場合、独立した会話履歴が得られます。

## パッケージレイアウト

```
cmd/anna/              エントリーポイント、CLIコマンド、サービスワイヤリング
internal/
  config/              Storeインターフェース、DBStore（SQLite）、Snapshot、タイプ
  ai/                  Message/Contentタイプ、Model、Providerインターフェース、ストリーミングイベント
  agent/               PoolManager、Pool、Session、ワークスペース設定、ランナーファクトリー
    engine/            エージェントループエンジン（マルチターンツール実行）
    runner/            GoRunner、システムプロンプトビルダー、スキルロード
  channel/             Channelインターフェース、アイデンティティ解決、スラッシュコマンド、通知
    cli/               Bubble Tea TUI
    telegram/          Telegramボット
    qq/                QQボット
    feishu/            飛書ボット
  admin/               HTTP API + 埋め込みSPA（templ + Alpine.js + daisyUI）
  auth/                RBAC/ABACポリシーエンジン、セッション、サンドボックス
  db/                  SQLite、Atlasマイグレーション、sqlcクエリ
  scheduler/           gocronサービス、ハートビート、スケジューラーツール
  memory/              メモリエンジン、コンパクション、検索、メモリツール
  skills/              スキルツール（skills.sh経由でsearch/install/list/remove）
pkg/
  tools/               Toolインターフェース、レジストリ、ビルトインツール（read、bash、write、edit、agent）
plugins/
  tools/               プラグインツールレジストリ + プラグインツール（webfetch）
  hooks/               プラグインフックレジストリ + プラグインフック（rtk）
  channels/            チャネルプラグイン（telegram、qq、feishu、weixin）
  providers/           プロバイダープラグインレジストリ + LLMアダプター（anthropic、openai、openai-response）
```

## 設定

設定はSQLiteに保存され、`config.Store`インターフェースを通じてアクセスされます。YAML設定ファイルはありません。すべての設定（プロバイダー、エージェント、チャネル、スケジューラー）は、管理APIまたはデータベース経由で管理されます。

- **Store**（`config.Store`）-- プロバイダー、エージェント、チャネル、ユーザー、チャット-エージェントバインディングの読み書きのためのインターフェース。`DBStore`によって実装されています。
- **DBStore**（`config.DBStore`）-- sqlc生成クエリを使用したSQLiteバックの実装。
- **Snapshot**（`config.Snapshot`）-- 単一エージェントの設定の読み取り専用ビュー。プール作成時にStoreから組み立てられます。解決されたプロバイダー認証情報、モデル名、ワークスペースパス、システムプロンプト、ランナー設定が含まれます。ランナーファクトリーとエージェントごとの設定が必要なツールに渡されます。

## マルチユーザー・マルチエージェントルーティング

各受信メッセージは、エージェントループに到達する前に2段階の解決を経ます：

1. **ユーザー解決**（`channel.ResolveUser`）-- 外部プラットフォームIDで送信者をアップサートし、安定した内部ユーザーIDを持つ`config.User`レコードを返します。
2. **エージェント解決**（`channel.ResolveAgent`）-- このメッセージを処理するエージェントを決定します：
   - DMでは、ユーザーの`default_agent_id`が使用されます。
   - グループチャットでは、`chat_agents`バインディングが`(platform, chat_id)`をエージェントにマップします。
   - どちらも設定されていない場合、最初の有効なエージェントがフォールバックとして使用されます。

解決されたユーザーとエージェントは、すべてのハンドラーとコマンドパスを通じてスレッド化される`ResolvedChat`構造体にバンドルされます。この構造体は、ターゲット`Pool`、`User`、`AgentID`、および`SessionKey`を保持します。

`PoolManager`は`map[agentID]*Pool`を維持し、最初のアクセス時にプールを遅延作成します。各プールは、ランナーファクトリー経由でエージェントの`Snapshot`（モデル、認証情報、ワークスペース、システムプロンプト）で設定されます。

### エージェント切り替え

`/agent`スラッシュコマンド（`AgentCommander`によって処理）により、ユーザーは有効なエージェントをリストし、DMまたはグループチャットのアクティブなエージェントを切り替えることができます。DMでは`default_agent_id`を更新し、グループでは`chat_agents`バインディングを更新します。`/model`は現在のエージェント内のセッションごとのままです。

## プロバイダー

LLMプロバイダーはプラグインベースです。Annaには3つのビルトインプロバイダーがあります：

| プロバイダー      | API                  | ユースケース                                    |
| ----------------- | -------------------- | ----------------------------------------------- |
| `anthropic`       | Messages API         | Claudeモデル                                    |
| `openai`          | Chat Completions API | GPTモデル                                       |
| `openai-response` | Responses API        | OpenAI互換サービス（Perplexity、Together.ai等） |

各プロバイダーは、ストリーミングレスポンス用の`ai.ProviderAdapter`インターフェースを実装し、オプションでモデル検出用の`ai.ModelLister`を実装します。すべてのプロバイダーは、`ImageContent`タイプを介してマルチモーダル入力（テキスト + 画像）をサポートし、ネイティブ画像フォーマット（Anthropic用のbase64ブロック、OpenAI用のデータURIimage_url）に変換します。

プロバイダーは`plugins/providers/`に配置され、`init()`で自己登録します。新しいプロバイダーの追加は`plugins/providers/`にパッケージを作成するだけで、他の接続コードは不要です。詳細は[プラグインシステム](/docs/features/plugin-system)を参照してください。

## ツール

Go runnerはLLM呼び出しにツールを注入します。ツールは`pkg/tools/`で定義された共通インターフェースに従います。`tools.Definition`タイプは`ai.ToolDefinition`の型エイリアスで、ドメインパッケージを分離します：

```go
type Tool interface {
    Definition() tools.Definition
    Execute(ctx context.Context, args map[string]any) (string, error)
}
```

### ビルトインツール（常に利用可能）

| ツール  | 説明                                          |
| ------- | --------------------------------------------- |
| `read`  | UTF-8セーフな切り詰めでファイル内容を読み取り |
| `bash`  | シェルコマンドの実行                          |
| `write` | ファイルのアトミックな作成/上書き             |
| `edit`  | コンテキストを保持したファイルセクションの編集 |
| `agent` | 制限されたサブタスクのサブエージェントループを生成 |

### プラグインツール（管理画面で切り替え可能）

| ツール     | 説明                |
| ---------- | ------------------- |
| `webfetch` | Webページ内容の取得 |

プラグインツールは`plugins/tools/`にあり、`init()`で自己登録します。新しいプラグインツールの追加にはブランクインポートのみが必要で、ワイヤリングコードの変更は不要です。完全なプラグインアーキテクチャについては[プラグインシステム](/docs/features/plugin-system)を参照してください。

### Agentツール

`agent`ツールは、エージェントが分離されたコンテキストで子エージェントループを生成できるようにします。これは、親の会話を汚染することなく、新鮮なコンテキストから利益を得る焦点を絞ったサブタスク（調査、コードレビュー、ドラフト作成）に役立ちます。

- 各子は、タスクの説明のみを含む新鮮なメッセージ履歴を取得
- 複数のタスクがgoroutine経由で並列実行（設定可能な並行数）
- 再帰を防ぐため、`agent`ツールは子から除外されます
- 子の出力は、親コンテキストの膨張を避けるために約4096トークンに切り詰められます
- YAMLフロントマター付きmarkdownファイルからのプリセット読み込みをサポート
- タスクごとのオプション：`preset`、`context`、`model`（オーバーライド）、`system`（追加の指示）、`tools`（ホワイトリスト）、`max_turns`（デフォルト10）、`timeout_seconds`（デフォルト120）

### 追加ツール（条件付き注入）

| ツール      | 条件                              | 説明                                                        |
| ----------- | --------------------------------- | ----------------------------------------------------------- |
| `memory`    | 常に                              | 統合メモリツール（grep/describe/expand/user_memory_update） |
| `skills`    | 常に                              | スキル管理（skills.shからsearch/install/list/remove）       |
| `scheduler` | `scheduler.enabled: true`         | タスクのスケジュール（add/list/removeジョブ）               |
| `notify`    | ゲートウェイモード + チャネル設定 | ディスパッチャー経由で通知を送信                            |

`memory`ツール内の`user_memory_update`アクションは、データベース内のユーザーごと・エージェントごとのメモリ内容全体を置き換える書き込み専用操作です。これらのノートは常にシステムプロンプト（「User Memory」セクション内）にロードされるため、エージェントはセッション間でユーザーの好みや重要な詳細について永続的なコンテキストを持ちます。これにより、以前のファイルベースのSOUL.md/USER.mdアプローチが、DBバックの`UserMemoryStore`に置き換えられます。

## セッションライフサイクル

1. チャネルがユーザーとエージェントを解決し、`ResolvedChat`を生成
2. `ResolvedChat.Pool.Chat(ctx, sessionKey, message)`が呼び出される -- メッセージは`string`（テキスト）または`[]ContentBlock`（マルチモーダル）
3. Poolがスコープされたキー`{agentID}:{platform}:{userID}:{context}`を使用してセッションを検索または作成
4. Poolがセッションのランナーを取得または作成し、エージェントのSnapshotで設定
5. Runnerがチャネル経由でイベントをストリームバック
6. アイドルタイムアウト時、ランナーは刈り取られます。セッションは`memory.Engine`経由でSQLiteに永続化されます

履歴管理については、[session-compaction.md](/docs/core/session-compaction)を参照してください。

## Channelインターフェース

すべてのメッセージングプラットフォームは`channel.Channel`インターフェースを実装します：

```go
type Channel interface {
    Name() string
    Start(ctx context.Context) error
    Stop()
    Notify(ctx context.Context, n Notification) error
}
```

共有コマンドロジック（`/new`、`/compact`、`/model`、`/agent`、`/whoami`）は`channel.HandleCommand`にあり、各チャネルがコアロジックのために委譲します。`/model`と`/agent`は、プラットフォーム固有のUI（Telegramはインラインキーボードを使用、QQ、Feishu、WeChatはテキストリストを使用、CLIはTUIピッカーを使用）が必要なため、チャネルごとに処理されます。

## 管理API

`internal/admin/`パッケージは、システムを管理するためのHTTP APIと埋め込みSPAを提供します。エンドポイントは、プロバイダー、エージェント、チャネル、ユーザー、セッション、スケジューラージョブ、グローバル設定のCRUD操作をカバーします。管理サーバーは`config.Store`を通じて読み書きし、オペレーターに以前YAMLファイルで行われていた設定のためのWebインターフェースを提供します。

## 通知フロー

```
Agent notify tool      --> Dispatcher --> Channel (Telegram/QQ/Feishu/WeChat)
Scheduler job result   --> Dispatcher --> Channel (Telegram/QQ/Feishu/WeChat)
```

ディスパッチャーはセットアップの早い段階で作成されますが、バックエンドはゲートウェイサービスの開始時に後で登録されます。PoolManagerは、`ExtraToolsFactory`を介してエージェントごとの通知ツール注入をワイヤするために使用されます。詳細については、[notification-system.md](/docs/features/notification-system)を参照してください。
