---
title: スケジューラーシステム
---

## ステータス

実装済み -- `internal/scheduler/`パッケージ（gocron/v2スケジューラー、SQLite永続化、管理パネルCRUD、エージェントツール）。

## 概要

Annaは、エージェントがリマインダーの設定、定期タスクの実行、繰り返し作業の自動化を行えるように、スケジュールされたタスク実行をサポートしています。スケジューラーシステムは、すべてのスケジューリングを[gocron/v2](https://github.com/go-co-op/gocron)に委譲し、その上に永続化、マルチエージェントルーティング、エージェント向けツールを追加します。

## アーキテクチャ

```
Agent (via tool call)
    |
    |  add / list / remove
    v
+----------+       +-------------+
| SchedulerTool | ----> |   Service   |
+----------+       +------+------+
                          |
              +-----------+-----------+
              |                       |
     gocron/v2 Scheduler     sched_jobs (SQLite)
              |
              v
        OnJobFunc callback
              |
              v
      PoolManager.Chat(ctx, agentID, userID, sessionID, message)
```

### パッケージ: `internal/scheduler/`

トップレベルパッケージ（`internal/`配下）。5つのファイル:

| ファイル                            | 目的                                                       |
| ----------------------------------- | ---------------------------------------------------------- |
| `internal/scheduler/job.go`         | `Job`と`Schedule`型                                        |
| `internal/scheduler/service.go`     | `Service` -- gocronラッパー、スケジューリング、ジョブCRUD  |
| `internal/scheduler/heartbeat.go`   | ハートビートポーリング -- LLMによる判断/実行/通知          |
| `internal/scheduler/persistence.go` | データベース永続化（ジョブのロード/保存/マイグレーション） |
| `internal/scheduler/tool.go`        | `SchedulerTool` -- `tool.Tool`を実装するエージェントツール |

### 主要な型

**Schedule**はジョブをいつ実行するかを定義します。正確に1つのフィールドを設定する必要があります。

- `cron` -- cron式（例: `"0 9 * * 1-5"`は平日午前9時）
- `every` -- Go duration（例: `"30m"`, `"2h"`, `"24h"`）
- `at` -- 1回限りのジョブのためのRFC3339タイムスタンプ（例: `"2024-01-15T14:30:00+08:00"`）

**Job**は永続化された定義です。

```go
type Job struct {
    ID          string    // 短いUUID
    Name        string    // 人間が読める名前
    Schedule    Schedule  // cron、間隔、または1回限り
    Message     string    // エージェントに送信されるプロンプト
    AgentID     string    // プール内のターゲットエージェント
    UserID      string    // 所有ユーザー
    SessionMode string    // "reuse"（デフォルト）または"new"
    Enabled     bool
    CreatedAt   time.Time
}
```

ジョブは`agent_id`と`user_id`フィールドを持つため、スケジューラーはPoolManager経由で各ジョブを正しいエージェントプールにルーティングできます。

### サービスライフサイクル

1. `scheduler.New(db)`または`scheduler.NewFromPath(dbPath)` -- SQLiteに支えられたスケジューラーを作成
2. `service.SetOnJob(fn)` -- コールバックを設定（循環依存を解決するための遅延配線）
3. `service.Start(ctx)` -- DBからジョブをロードし、すべてをgocronに登録し、スケジューラーを開始
4. `service.Stop()` -- スケジューラーをシャットダウン（`NewFromPath`経由で開かれた場合はDBも閉じる）

### 永続化

ジョブは共有SQLiteデータベース（`~/.anna/anna.db`）の`sched_jobs`テーブルに保存されます。各変更（追加/削除）は個別のINSERT/DELETEです。ファイル全体の書き換えはありません。

初回起動時に、レガシーの`jobs.json`ファイル（DB以前のバージョンから）が存在する場合、ジョブは自動的にデータベースに移行され、ファイルは削除されます。

### 1回限りのジョブ

`at`でスケジュールされたジョブは、指定された時刻に正確に1回実行され、実行後にスケジューラーとデータベースの両方から自動的に削除されます。これにより、古いエントリなしでジョブリストをクリーンに保ちます。

動作の詳細:

- `at`フィールドは、タイムゾーンオフセット付きの有効なRFC3339タイムスタンプである必要があります
- 過去のタイムスタンプは作成時に拒否されます
- Annaが再起動し、1回限りのジョブのタイムスタンプが既に過ぎている場合、ジョブは静かにスキップされます（スケジュールされない）が、手動で削除されるまでデータベースに残ります
- 実行が成功すると、スケジューラーのブロックを避けるためにクリーンアップは非同期に実行されます

### セッションモデル

各スケジュールされたジョブのセッション動作は、その`session_mode`によって制御されます。

- **`reuse`**（デフォルト）-- ジョブは安定したセッションID `{agentID}:scheduler:{job.ID}`を取得します（agentIDが設定されている場合にプレフィックスされます）。エージェントは同じジョブのスケジュールされた実行間で会話メモリを保持します。
- **`new`** -- 各実行は一意のセッションID `scheduler:{job.ID}:{timestamp}`を取得します。エージェントは毎回、以前のコンテキストなしで新鮮に開始します。

## 設定

スケジューラー設定は管理パネルで管理されます。設定はデータベースの`settings`テーブルに保存されます。管理パネルUIからスケジューラーを有効/無効にし、その動作を設定します。

スケジューラーは次の場合にのみアクティブです。

- 管理パネル設定でスケジューラーが有効になっている
- `runner.type`が`go`である（Piランナーはカスタムツールをサポートしていません）

### 管理パネルAPI

管理パネルは、スケジューラージョブの完全なCRUD APIを公開します。

| メソッド | エンドポイント             | 説明                                         |
| -------- | -------------------------- | -------------------------------------------- |
| `GET`    | `/api/scheduler/jobs`      | すべてのスケジュールされたジョブをリスト表示 |
| `POST`   | `/api/scheduler/jobs`      | 新しいジョブを作成                           |
| `PUT`    | `/api/scheduler/jobs/{id}` | 既存のジョブを更新                           |
| `DELETE` | `/api/scheduler/jobs/{id}` | ジョブを削除                                 |

## エージェントツール

`scheduler`ツールは、スケジューラーが有効な場合、Goランナーに自動的に登録されます。エージェントは3つのアクションを持つツール呼び出しを介してそれを使用します。

### `add` -- ジョブを作成

パラメータ:

- `name`（必須）-- 人間が読める名前
- `message`（必須）-- 各実行時に実行する指示
- `cron` -- cron式（これOR `every` OR `at`を使用）
- `every` -- Go duration（これOR `cron` OR `at`を使用）
- `at` -- 1回限りのジョブのためのRFC3339タイムスタンプ（これOR `cron` OR `every`を使用）
- `session_mode` -- `"reuse"`（デフォルト）は会話履歴を保持; `"new"`は各実行で新鮮に開始

例（繰り返し）: _"30分ごとにメールをチェックするリマインダーを設定して"_ トリガー:

```json
{
  "action": "add",
  "name": "email check",
  "message": "Check my email and summarize new messages",
  "every": "30m"
}
```

例（1回限り）: _"午後2時40分に北京の天気をチェックするようリマインドして"_ トリガー:

```json
{
  "action": "add",
  "name": "weather reminder",
  "message": "Check Beijing weather and send me a summary",
  "at": "2024-01-15T14:40:00+08:00"
}
```

### `list` -- すべてのジョブをリスト表示

パラメータなし。すべてのスケジュールされたジョブをJSONとして返します。

### `remove` -- ジョブを削除

パラメータ:

- `id`（必須）-- `add`または`list`からのジョブID

## ハートビート

ハートビートは、スケジューラーサービスによって管理される組み込みの定期タスクです。`HEARTBEAT.md`ファイルをポーリングし、LLMを使用してアクションが必要かどうかを判断し、指示を実行し、通知ディスパッチャー経由で結果を送信します。

### 動作方法

1. `SetHeartbeat(cfg, chatFn, notifier)`でスケジューラーサービスにハートビートを設定
2. `StartHeartbeat(ctx, every)`で`ScheduleEvery`経由でポーリングループをスケジュール
3. 各ティック:
   - ハートビートファイルを読み込む（欠落または空の場合はスキップ）
   - 内容をファストモデルに送信して`skip`/`run`判断（ツール使用不可）
   - `run`の場合、実行のためにメインセッションに内容を送信
   - 通知ディスパッチャー経由で結果を配信

### 設定

ハートビート設定は管理パネルで設定されます。以下のパラメータが利用可能です。

- **enabled** -- ハートビートポーリングがアクティブかどうか（デフォルト: false）
- **every** -- Go durationとしてのポーリング間隔（例: `10m`）
- **file** -- ハートビートファイルへのパス、絶対パスでない限りワークスペースからの相対パス（例: `HEARTBEAT.md`）

ハートビートは`anna gateway`モードでのみ実行されます。コストを最小限に抑えるため、ゲート判断にはファストモデルが使用されます。

## 配線

スケジューラーシステムは、`main.go`での遅延配線を介して循環依存（サービスはコールバックのためにプールが必要、ランナーはツールが必要）を解決します。

1. コールバックなしで`scheduler.Service`を作成
2. `scheduler.NewTool(service)`を作成し、`ExtraTools`経由でランナーに渡す
3. ランナーファクトリーでPoolManagerを作成
4. ジョブの`agent_id`と`user_id`を使用してPoolManager経由でルーティングするコールバックで`service.SetOnJob(...)`を呼び出す
5. ハートビートが有効な場合、チャット関数とnotifierで`service.SetHeartbeat(...)`を呼び出す
6. ゲートウェイで`service.Start(ctx)`（またはハートビートのみモードの場合は`StartEphemeral`）を呼び出す
7. チャネルが配線された後に`service.StartHeartbeat(ctx, every)`を呼び出す

`wireSchedulerNotifier`関数は、単一プールの代わりにPoolManager経由でジョブ実行をルーティングし、各ジョブの`agent_id`と`user_id`を使用して正しいエージェントに到達します。

## テスト

テストは`internal/scheduler/scheduler_test.go`と`internal/scheduler/heartbeat_test.go`にあり、以下をカバーしています。

- 追加、リスト、削除のライフサイクル
- 入力検証（空の名前、スケジュール欠落、無効なduration、競合するスケジュールフィールド、無効/過去のタイムスタンプ）
- 存在しないジョブの削除
- サービス再起動時の永続性
- スケジュールでのコールバック起動
- 1回限りのジョブ作成と検証
- 1回限りのジョブが正確に1回起動し自動削除
- 過去のタイムスタンプを持つ1回限りのジョブは再起動時にスキップ
- 1回限りのジョブのためのツールインターフェース
- セッションモードのデフォルト、reuse、new、および無効検証
- ツールインターフェース経由のセッションモード
- 完全なツールインターフェース（`Execute`経由でadd/list/remove）
- エラーケース（無効なアクション、ID欠落）
- ハートビート: ファイルが欠落している場合はスキップ
- ハートビート: 判断にファストモデル使用
- ハートビート: run判断が実行し通知
- ハートビート: 判断がツールを使用した場合のエラー
- ハートビート: notifierエラーが伝播

実行方法:

```bash
go test -race ./internal/scheduler/
```
