---
title: プラグインシステム
---

## 概要

Annaはコンパイル組み込みのプラグインモデルを採用しています。すべてのプラグインはannaバイナリに直接コンパイルされています。サブプロセスプロトコル、別プロセス、サードパーティプラグインのインストールはありません。プラグインは`plugins/`以下の標準インターフェースを実装するGoパッケージです。

2種類のプラグイン：

- **ツールプラグイン**はLLMエージェントが呼び出せるツールを提供します（例：`webfetch`）。
- **チャネルプラグイン**はメッセージングプラットフォーム統合を提供します（例：`telegram`、`qq`、`feishu`、`weixin`）。

注意：コアツール（`read`、`bash`、`edit`、`write`）は常に有効で、プラグインではありません。

## ビルトインプラグイン

Annaには5つのビルトインプラグインがあります：

| 種類    | 名前       | 説明                           |
| ------- | ---------- | ------------------------------ |
| tool    | webfetch   | Webページ取得                  |
| channel | telegram   | Telegramボット                 |
| channel | qq         | QQボット                       |
| channel | feishu     | 飛書（Lark）ボット             |
| channel | weixin     | WeChatボット（iLink経由）      |

## ストレージ

プラグインの状態はデータベースの`settings_plugins`テーブルに保存されます。各プラグインには以下が含まれます：

| フィールド | 型         | 説明                                              |
| ---------- | ---------- | ------------------------------------------------- |
| `id`       | string     | プラグインID（`種類/名前`、例：`tool/webfetch`）  |
| `kind`     | string     | `tool`または`channel`                             |
| `name`     | string     | プラグイン名                                      |
| `enabled`  | bool       | プラグインが有効かどうか                          |
| `config`   | JSON map   | プラグイン固有の設定（トークン、キーなど）        |

## CLIコマンド

```bash
anna plugin list               # すべてのプラグインとステータスをリスト表示
anna plugin enable <id>        # プラグインを有効化
anna plugin disable <id>       # プラグインを無効化
anna plugin config <id>        # プラグイン設定の表示
anna plugin config <id> k=v    # プラグイン設定のキーバリューペアを設定
```

## 管理パネル

チャネルプラグイン（Telegram、QQ、飛書、WeChat）は管理パネル（`anna --open`）で設定します。管理パネルは`settings_plugins`テーブルに書き込み、トークン、キー、チャネル固有の設定を管理するUIを提供します。
