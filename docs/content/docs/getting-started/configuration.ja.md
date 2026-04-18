---
title: 設定
---

すべての設定は、`~/.anna/anna.db`にある単一のSQLiteデータベースに保存されます。YAMLの設定ファイルはありません。設定をセットアップまたは変更するには、`anna --open`を実行してWeb管理パネルを開きます。

ホームディレクトリのデフォルトは`~/.anna`で、環境変数`ANNA_HOME`を設定することで変更できます。

## データベーステーブル

設定はSQLiteデータベース内の複数のテーブルに分散されています。

### settings

グローバル設定のキーバリューストア。各行には`key`(テキスト)と`value`(JSONテキスト)があります。既知のキー:

| キー           | 説明                                                               |
| -------------- | ------------------------------------------------------------------ |
| `runner`       | ランナータイプ、システムプロンプト、アイドルタイムアウト、圧縮設定 |
| `compaction`   | 圧縮のしきい値(max_tokens、keep_tail)                              |
| `heartbeat`    | ハートビートのポーリング切り替えと間隔                             |
| `plugins`      | プラグイン定義のJSON配列                                           |
| `models_cache` | プロバイダーからキャッシュされたモデルリスト                       |

### settings_agents

エージェントごとに1行。

| カラム          | 型      | 説明                                                     |
| --------------- | ------- | -------------------------------------------------------- |
| `id`            | TEXT    | エージェントスラッグ(例: `anna`)                         |
| `name`          | TEXT    | 表示名                                                   |
| `model`         | TEXT    | `provider/model`形式のデフォルトモデル                   |
| `model_strong`  | TEXT    | `provider/model`形式の強力層モデル                       |
| `model_fast`    | TEXT    | `provider/model`形式の高速層モデル                       |
| `system_prompt` | TEXT    | カスタムシステムプロンプト(デフォルトビルダーをバイパス) |
| `workspace`     | TEXT    | エージェントワークスペースディレクトリの絶対パス         |
| `enabled`       | INTEGER | 1 = アクティブ、0 = 無効                                 |

### settings_channels

メッセージングプラットフォームごとに1行。

| カラム    | 型      | 説明                                                     |
| --------- | ------- | -------------------------------------------------------- |
| `id`      | TEXT    | プラットフォーム識別子: `telegram`、`qq`、`feishu`、または`weixin` |
| `enabled` | INTEGER | 1 = アクティブ、0 = 無効                                 |
| `config`  | TEXT    | プラットフォーム固有の設定を含むJSONブロブ(下記参照)     |

### settings_users

外部プラットフォームユーザーをエージェントにマッピング。

| カラム             | 型      | 説明                                                               |
| ------------------ | ------- | ------------------------------------------------------------------ |
| `id`               | INTEGER | 自動インクリメントプライマリキー                                   |
| `external_id`      | TEXT    | プラットフォーム上のユーザーID                                     |
| `platform`         | TEXT    | プラットフォーム識別子(例: `telegram`)                             |
| `name`             | TEXT    | 表示名                                                             |
| `default_agent_id` | TEXT    | `settings_agents.id`へのFK -- このユーザーのデフォルトエージェント |

### settings_channel_agents

特定のグループチャットを特定のエージェントにルーティング。

| カラム     | 型   | 説明                                         |
| ---------- | ---- | -------------------------------------------- |
| `platform` | TEXT | プラットフォーム識別子                       |
| `chat_id`  | TEXT | プラットフォーム上のグループまたはチャットID |
| `agent_id` | TEXT | `settings_agents.id`へのFK                   |

複合プライマリキー: `(platform, chat_id)`。

## ランナー設定

JSONオブジェクトとして`settings`テーブルの`runner`キーに保存されます。

| フィールド              | デフォルト | 説明                                                     |
| ----------------------- | ---------- | -------------------------------------------------------- |
| `type`                  | `go`       | ランナー実装(現在は`go`のみ)                             |
| `system`                | `""`       | カスタムシステムプロンプト(デフォルトビルダーをバイパス) |
| `idle_timeout`          | `10`       | アイドルランナーを停止するまでの分数                     |
| `compaction.max_tokens` | `80000`    | 履歴がこれを超えた場合に自動圧縮                         |
| `compaction.keep_tail`  | `20`       | 圧縮後に保持する最近のメッセージ数                       |

## チャネル設定ブロブ

各プラットフォームは、`settings_channels`の`config`カラムに独自のJSON構造を保存します。

**Telegram**

```json
{
  "token": "BOT_TOKEN",
  "enable_notify": false,
  "notify_chat": "123456789",
  "channel_id": "@my_channel",
  "group_mode": "mention",
  "allowed_ids": [136345060]
}
```

**QQ**

```json
{
  "app_id": "QQ_BOT_APP_ID",
  "app_secret": "QQ_BOT_APP_SECRET",
  "enable_notify": false,
  "group_mode": "mention",
  "allowed_ids": []
}
```

**Feishu**

```json
{
  "app_id": "FEISHU_APP_ID",
  "app_secret": "FEISHU_APP_SECRET",
  "encrypt_key": "",
  "verification_token": "",
  "enable_notify": false,
  "notify_chat": "oc_xxx",
  "group_mode": "mention",
  "allowed_ids": []
}
```

`group_mode`は`mention`、`always`、または`disabled`を受け付けます。

## ディレクトリレイアウト

| パス                                         | 目的                                              | カテゴリ   |
| -------------------------------------------- | ------------------------------------------------- | ---------- |
| `~/.anna/anna.db`                            | SQLiteデータベース(設定、メモリ、スケジューラ)    | データ     |
| `~/.anna/workspaces/{agent-id}/skills/`      | エージェントごとのインストール済みスキル          | データ     |
| `~/.anna/workspaces/{agent-id}/anna.log`     | エージェントごとのログファイル                    | データ     |
| `~/.anna/workspaces/{agent-id}/SOUL.md`      | オプションのソウル/アイデンティティオーバーライド | データ     |
| `~/.anna/workspaces/{agent-id}/SYSTEM.md`    | オプションのシステムプロンプトオーバーライド      | データ     |
| `~/.anna/workspaces/{agent-id}/HEARTBEAT.md` | ハートビート指示                                  | データ     |
| `~/.anna/cache/`                             | モデルキャッシュ(削除可能)                        | キャッシュ |

- **anna.db**は、すべての設定、メモリ、スケジューラデータの唯一の信頼できる情報源です。
- **workspaces/**にはエージェントごとのデータが含まれます。各エージェントはエージェントIDをキーとする独自のディレクトリを取得します。
- **cache/**には再生成可能なデータが含まれます。`anna models update`を実行して再構築します。
- **エージェントサンドボックス設定**は各エージェントレコード（`settings_agents.sandbox`）に保存されます。管理パネルからネットワークポリシーを編集できます。backendも保存済みJSONで直接設定可能です。
  - `backend`：`auto`（デフォルト）、`boxsh`、`local`、または `docker`
  - `network.mode`：`disabled`（デフォルト）、`allow_all`、または `whitelist`
  - `network.allowlist`：modeが `whitelist` の場合のみ必須
  - LinuxおよびmacOSでは `boxsh` を選択した際に検証が行われます。サンドボックスバックエンドが利用できないか、設定されたネットワークモードを強制できない場合、ランナーの起動はフェイルクローズします。
  - `local` はアドバイザリーな強制のみを行う緩やかなバックエンドです。明示的なローカル/開発シナリオにのみ使用してください。
  - 現在の `boxsh` クライアントビルドは実行時に `whitelist` モードを拒否する場合があります。ランタイムがホワイトリスト強制をサポートしている場合のみ使用してください。
  - `docker` は `sandbox.docker.image`（デフォルト `alpine:3.20`）から起動した専用コンテナで各セッションを実行します。オプトイン型であり、`auto` では自動選択されません。`PATH` に `docker` CLI があり、daemonに到達できる必要があります。Windows（`boxsh` が利用できない場合）や特定のLinuxユーザースペースを必要とするワークフローで有用です。
  - Dockerバックエンドの設定は `sandbox.docker` の下に置きます：
    - `image`：Dockerイメージ参照。デフォルトは `alpine:3.20`。
    - `user`：`docker --user` に渡すオプションの `uid:gid` 文字列。Linux/macOSではannaプロセスのuid/gidをデフォルトとし、Windowsでは空（Docker Desktopがマッピングを処理）。コンテナ内で `root` が必要な場合はオーバーライドしてください。
    - `allow_pull`：`true` の場合、プリフライト時に不足しているイメージを自動的にプルします。デフォルトは `false`（イメージが不足している場合はフェイルファスト）。
    - `extra_mounts`：`host:container[:ro]` 形式の追加バインドマウント文字列。パスは絶対パスでなければなりません。
  - Dockerバックエンドは現在 `whitelist` ネットワークモードやHTTPメディエーションを実装していません — `boxsh` と同様にフェイルクローズします。`disabled` または `allow_all` を使用してください。
  - Dockerはワークスペースルートを `/workspace` にバインドマウントし、各読み取り専用パスを `/workspace-readonly/<index>` にマウントします。ホストの絶対パスに依存するスクリプトはコンテナ内パスを使用する必要があります。

dockerバックエンドのエージェント `sandbox` フィールドのJSON例：

```json
{
  "backend": "docker",
  "network": { "mode": "allow_all" },
  "docker": {
    "image": "alpine:3.20",
    "user": "1000:1000",
    "allow_pull": true,
    "extra_mounts": ["/var/cache/build:/cache:ro"]
  }
}
```

## 環境変数

すべての設定フィールドに対する古い`ANNA_*`プレフィックスオーバーライドは削除されました。次の環境変数のみが認識されます:

| 変数                | 目的                                                      |
| ------------------- | --------------------------------------------------------- |
| `ANNA_HOME`         | ホームディレクトリをオーバーライド(デフォルトは`~/.anna`) |
| `ANTHROPIC_API_KEY` | AnthropicプロバイダーのフォールバックAPIキー              |
| `OPENAI_API_KEY`    | OpenAIプロバイダーのフォールバックAPIキー                 |

その他のすべての設定は、管理パネル(`anna --open`)またはデータベースを直接介して設定する必要があります。

## メモリデフォルト

ロスレスコンテキスト管理の設定は、現在ハードコードされたデフォルトです。今後のリリースで設定可能になる予定です。

| 設定              | デフォルト | 説明                                                 |
| ----------------- | ---------- | ---------------------------------------------------- |
| Fresh tail count  | `20`       | コンテキスト内に逐語的に保持される最近のメッセージ数 |
| Context threshold | `0.75`     | 圧縮をトリガーするコンテキストウィンドウの割合       |
| Leaf chunk size   | `10`       | リーフサマリごとにグループ化されるメッセージ数       |

## ハートビート

ハートビートはゲートウェイモード(`anna`)でのみ実行されます。設定は`settings`テーブルの`heartbeat`キーに保存されます。各ティックでは、最初に高速モデルを使用して`skip`または`run`を決定し、`run`の決定のみがメインハートビートセッションに送信され、その後ノティファイアを通じて配信されます。指示はエージェントの`HEARTBEAT.md`ファイルから読み取られます。

## プラグイン

プラグインは、`settings`テーブルの`plugins`キーにJSON配列として保存されます。各エントリには、JSファイルへの`path`とオプションの`config`オブジェクトがあります:

```json
[
  { "path": "~/plugins/hello.js" },
  { "path": "/abs/path/notify.js", "config": { "webhook_url": "https://example.com" } }
]
```
