---
title: モデル管理
---

## 階層化モデル

annaの各エージェントは、データベース（`settings_agents`テーブル）に保存される3つのモデルフィールドを持ちます。すべてのモデルフィールドのフォーマットは`provider/model`です（例：`anthropic/claude-sonnet-4-6`）。

| フィールド     | ユースケース                   |
| -------------- | ------------------------------ |
| `model`        | エージェントのデフォルトモデル |
| `model_strong` | 高度な推論、複雑なタスク       |
| `model_fast`   | 迅速な応答、シンプルなクエリ   |

`model_strong`と`model_fast`の両方は、設定されていない場合は`model`にフォールバックします。これらは管理パネル（`anna --open`）を通じてエージェントごとに設定します。

## プロバイダー設定

プロバイダーは管理パネル（`anna --open`）を通じて設定されます。各プロバイダーは、オプションのAPIキーとベースURLを持つ`settings_providers`テーブルに保存されます。

環境変数は、プロバイダーの`api_key`フィールドがデータベースで空の場合のフォールバックとして機能します：

| プロバイダー | 環境変数            | オプション変数    |
| ------------ | ------------------- | ----------------- |
| Anthropic    | `ANTHROPIC_API_KEY` |                   |
| OpenAI       | `OPENAI_API_KEY`    | `OPENAI_BASE_URL` |
| OpenAI互換   | `OPENAI_API_KEY`    | `OPENAI_BASE_URL` |

OpenAI互換プロバイダー（`openai-response`）は、PerplexityやTogether.aiなど、OpenAI Responses APIを実装する任意のサービスをサポートします。

## CLIコマンド

```bash
anna models             # 利用可能なモデルをリスト（listのエイリアス）
anna models list        # プロバイダー別にグループ化されたすべてのモデルをリスト
anna models update      # プロバイダーAPIからモデルを取得し、キャッシュを更新
anna models current     # アクティブなプロバイダー/モデルを表示
anna models set <p/m>   # デフォルトエージェントのモデルを切り替え（例：anna models set openai/gpt-4o）
anna models search <q>  # 名前でモデルを検索
```

### モデルキャッシュ

`anna models update`はすべての設定済みプロバイダーAPIにクエリを実行し、結果を`models_cache`キー下の`settings`テーブルに保存します。キャッシュは`list`、`search`、Telegramモデルピッカーで使用されます。

管理パネルからキャッシュを更新することもできます。

## ランタイム切り替え

モデルは再起動せずにランタイムで切り替えることができます：

- **CLI**: チャットセッション中の`/model`コマンド
- **Telegram**: インラインキーボードモデルピッカー
- **CLIコマンド**: `anna models set provider/model`はデフォルトエージェントのモデルをデータベースで更新します

## モデルメタデータ

モデルキャッシュが（`anna models update`または管理パネル経由で）入力されると、各モデルエントリにはプロバイダーAPIから取得されたメタデータが含まれます：

- モデルID
- 推論機能
- サポートされる入力タイプ（テキスト、画像）
- コンテキストウィンドウサイズ
- 最大出力トークン
- トークンごとのコスト（入力、出力、キャッシュ読み取り、キャッシュ書き込み）
- カスタムヘッダー

このメタデータは、モデル解決、表示、コスト追跡に使用されます。
