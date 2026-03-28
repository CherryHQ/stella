---
title: Feishu Bot
---

anna には WebSocket 接続の Feishu（Lark）ボットが含まれているため、公開 webhook は不要です。現在の Feishu 連携はチャット専用です。メッセージ、ストリーミング返信、スレッド、グループ、通知は anna が処理し、カレンダーやドキュメントなどのワークスペース操作は `lark-cli` に移行しました。

## セットアップ

1. [Feishu Open Platform](https://open.feishu.cn/) でアプリを作成します。
2. **Bot** 機能を有効にします。
3. **Event Subscriptions** で次を追加します。
   - `im.message.receive_v1`
   - リアクションイベントが必要なら `im.message.reaction.created_v1`
4. App ID、App Secret、Encrypt Key、Verification Token を控えます。
5. `anna --open` を実行し、管理画面で Feishu チャンネルを設定します。
6. anna を起動します。

```bash
anna
```

## Lark ワークスペース自動化

以前の組み込み `feishu_*` ツールと `/auth` フローは削除されました。

カレンダー、タスク、Docs、Wiki、Sheets、Drive、連絡先などの操作は、必要に応じて自分で `lark-cli` skill を追加し、外部の [`lark-cli`](https://github.com/larksuite/cli) と組み合わせて使ってください。

代表的な初期設定:

```bash
command -v lark-cli
npm install -g @larksuite/cli
lark-cli config init --new
lark-cli auth login --recommend
lark-cli auth status
```

ユーザーが追加した `lark-cli` skill は、旧 `feishu_calendar`、`feishu_task`、`feishu_im`、`feishu_doc`、`feishu_wiki`、`feishu_sheets`、`feishu_drive`、`feishu_bitable`、`feishu_user`、`feishu_search` の用途を置き換えられます。

## マルチユーザー対応

Feishu ユーザーはプラットフォーム ID から自動的に解決されます。セッションはユーザーごと・agent ごとに分離されるため、記憶や既定 agent はユーザー単位で保持されます。

## ストリーミング返信

ボットはメッセージをその場で更新しながら返信します。

1. まずプレースホルダーを送信
2. 生成中に内容を更新
3. 最終結果と処理時間を表示

ツール実行中の状態もストリーム内で簡潔に表示されます。

## 対応メッセージ型

| 種類 | 動作 |
| --- | --- |
| テキスト | そのまま LLM に送信 |
| 画像 | ダウンロードしてマルチモーダル入力として送信 |
| Post | リッチテキスト JSON をそのまま転送 |
| 音声 | 長さ付きの説明文に変換 |
| 動画 | 長さ付きの説明文に変換 |
| ファイル | ファイル情報付きの説明文に変換 |
| スタンプ | 説明文に変換 |
| 位置情報 | 可能なら座標付き説明文に変換 |
| 共有チャット/共有ユーザー | 説明文に変換 |
| 結合転送 | 要約マーカーに変換 |

## ネイティブスレッド

Feishu スレッド内でメッセージが送られた場合、anna は同じスレッド内で返信し、そのスレッド root 単位でセッションを分けます。スレッド外は親チャットのセッションを使います。

## グループ挙動

`group_mode` でグループ内の応答条件を制御します。

- `mention`: @ されたときだけ返信
- `always`: すべてのメッセージに返信
- `disabled`: グループでは返信しない

`groups` を使えば特定グループごとの上書きもできます。

## コマンド

Feishu では標準チャットコマンドを利用できます。

| コマンド | 説明 |
| --- | --- |
| `/new` | 新しいセッションを開始 |
| `/compact` | 現在の履歴を圧縮 |
| `/model` | モデル一覧または切り替え |
| `/agent` | agent 一覧または切り替え |
| `/whoami` | 自分のプラットフォーム ID を表示 |

## 設定例

```json
{
  "app_id": "FEISHU_APP_ID",
  "app_secret": "FEISHU_APP_SECRET",
  "encrypt_key": "",
  "verification_token": "",
  "group_mode": "mention",
  "enable_notify": false,
  "groups": {
    "oc_example": {
      "group_mode": "always",
      "system_prompt": "このグループではインフラ担当として答えてください。"
    }
  }
}
```

| フィールド | 説明 |
| --- | --- |
| `app_id` | Feishu アプリの App ID |
| `app_secret` | Feishu アプリの App Secret |
| `encrypt_key` | 任意のイベント暗号化キー |
| `verification_token` | 任意のイベント検証トークン |
| `group_mode` | 既定のグループ挙動。`mention`、`always`、`disabled` |
| `enable_notify` | scheduler や `notify` の出力先として Feishu を許可 |
| `groups` | Feishu `chat_id` ごとの上書き設定 |
