---
title: Feishu Bot
---

anna には、WebSocket 経由で接続する Feishu (Lark) ボットが含まれています（永続的な接続、公開 URL は不要）。

## セットアップ

1. [Feishu Open Platform](https://open.feishu.cn/) で Feishu アプリを作成します
2. アプリ設定で **Bot** 機能を有効にします
3. **Event Subscriptions** で `im.message.receive_v1` イベントを追加します
4. アプリ設定から App ID、App Secret、Encrypt Key、Verification Token を取得します
5. `anna onboard` を実行して管理パネルを起動します
6. 管理パネルで、AI プロバイダーを追加してから、アプリ認証情報を使用して Feishu チャンネルを設定します
7. ゲートウェイを起動します:

```bash
anna gateway
```

すべてのチャンネル設定（認証情報、グループモード、許可された ID など）は管理パネルから管理されます。環境変数は、プロバイダー API キー（`ANTHROPIC_API_KEY`、`OPENAI_API_KEY`）と `ANNA_HOME` に制限されています。

## マルチユーザーサポート

各 Feishu ユーザーは、プラットフォーム ID から自動的に解決されます。セッションは、エージェントごとにユーザーごとにスコープされます。手動でのユーザー設定は不要です。Feishu チャンネルは現在デフォルトエージェントを使用しています（`/agent` コマンドはまだ Feishu では利用できません）。

## ストリーミングレスポンス

ボットは、Edit-in-Place ストリーミングのために Feishu の Message Update API を使用します。LLM がトークンを生成すると、ボットは初期返信を送信し、新しいコンテンツで徐々に更新し、スムーズなストリーミング体験を提供します。

### ツールインジケーター

ツール実行中、ストリームは絵文字インジケーターでステータスを表示します:

| Tool     | Emoji            |
| -------- | ---------------- |
| `bash`   | lightning        |
| `read`   | book             |
| `write`  | pencil           |
| `edit`   | wrench           |
| `search` | magnifying glass |

## サポートされているメッセージタイプ

| Type             | Behavior                                                      |
| ---------------- | ------------------------------------------------------------- |
| Text             | 抽出されて LLM に送信                                         |
| Image            | ダウンロード、base64 エンコード、マルチモーダル入力として送信 |
| Post (rich text) | 完全なコンテキストのために生の JSON が LLM に渡される         |

## グループサポート

起動時に、ボットは Feishu Bot Info API 経由で自身の `open_id` を取得します。これにより、グループでの信頼性の高い @メンション検出が可能になり、ボットが自分自身のメッセージに応答すること（無限ループ）を防ぎます。

グループチャットでは、ボットは @メンションに応答します。管理パネルでグループモードを設定します:

- `mention` -- @メンションに応答（デフォルト）
- `always` -- すべてのグループメッセージに応答
- `disabled` -- グループメッセージを完全に無視

## アクセス制御

管理パネルで許可された open_id を追加することで、ボットとやり取りできるユーザーを制限します。空のままにするとすべてのユーザーを許可します。open_id を取得するには `/whoami` コマンドを使用します。

## 通知

管理パネルでプロアクティブ通知（スケジューラー結果、エージェントによってトリガーされたアラート）用のデフォルト通知チャットを設定します。

## コマンド

これらのコマンドをテキストメッセージとしてボットに送信します:

| Command             | Description                  |
| ------------------- | ---------------------------- |
| `/start` or `/help` | ウェルカムとヘルプ           |
| `/new`              | 新しいセッションを開始       |
| `/compact`          | 会話履歴を圧縮               |
| `/model`            | 利用可能なモデルを一覧表示   |
| `/model <number>`   | 番号でモデルに切り替え       |
| `/model <query>`    | 名前でモデルをフィルタリング |
| `/whoami`           | 設定用のユーザー ID を表示   |

## 設定リファレンス

以下のすべての設定は、`anna onboard` 管理パネルから管理されます。

| Field                | Description                                     | Default    |
| -------------------- | ----------------------------------------------- | ---------- |
| `app_id`             | Feishu App ID                                   | (required) |
| `app_secret`         | Feishu App Secret                               | (required) |
| `encrypt_key`        | イベント暗号化キー（Events & Callbacks から）   | (optional) |
| `verification_token` | イベント検証トークン（Events & Callbacks から） | (optional) |
| `notify_chat`        | プロアクティブ通知用のチャット ID               | (optional) |
| `group_mode`         | グループ動作: `mention`、`always`、`disabled`   | `mention`  |
| `allowed_ids`        | 許可されたユーザー open_id（空 = すべて）       | `[]`       |
