---
title: プラグインシステム
---

## 概要

Annaは統一されたサブプロセスプラグインモデルを採用しています。すべてのプラグイン（ビルトインおよびユーザーインストール）は、バージョン管理されたstdioプロトコル（JSON-RPCスタイル）でannaと通信する別プロセスです。JavaScriptプラグインやインプロセスフックはありません。

2種類のプラグイン：

- **ツールプラグイン**はLLMエージェントが呼び出せるツールを提供します（例：`read`、`bash`、`edit`、`write`、`webfetch`）。
- **チャネルプラグイン**はメッセージングプラットフォーム統合を提供します（例：`telegram`、`qq`、`feishu`、`weixin`）。

## ビルトインプラグイン

Annaには9つのビルトインプラグインがバイナリにコンパイルされています：

| 種類    | 名前       | 説明                           |
| ------- | ---------- | ------------------------------ |
| tool    | read       | ファイル読み取り               |
| tool    | bash       | シェルコマンド実行             |
| tool    | edit       | ファイル編集（検索と置換）     |
| tool    | write      | ファイル書き込み               |
| tool    | webfetch   | Webページ取得                  |
| channel | telegram   | Telegramボット                 |
| channel | qq         | QQボット                       |
| channel | feishu     | 飛書（Lark）ボット             |
| channel | weixin     | WeChatボット（iLink経由）      |

ビルトインプラグインはユーザーインストールプラグインと同じサブプロセスプロトコルを使用します。同名のプラグインをインストールすることで置き換えることができます。

## プラグインマニフェスト

すべてのプラグインは`plugin.json`マニフェストで定義されます：

```json
{
  "name": "my-tool",
  "version": "1.0.0",
  "kind": "tool",
  "protocol_version": "1",
  "description": "プラグインの機能説明。",
  "entrypoint": "./my-tool-binary",
  "tools": [
    {
      "name": "my_tool",
      "description": "LLM向けのツール説明。",
      "input_schema": {
        "type": "object",
        "properties": {
          "query": { "type": "string", "description": "検索クエリ" }
        }
      }
    }
  ]
}
```

## ストレージ

プラグインの状態はデータベースの`settings_plugins`テーブルに保存されます。各プラグインには以下が含まれます：

| フィールド | 型         | 説明                                              |
| ---------- | ---------- | ------------------------------------------------- |
| `id`       | string     | プラグインID（`種類/名前`、例：`tool/read`）      |
| `kind`     | string     | `tool`または`channel`                             |
| `name`     | string     | プラグイン名                                      |
| `enabled`  | bool       | プラグインが有効かどうか                          |
| `config`   | JSON map   | プラグイン固有の設定（トークン、キーなど）        |

## CLIコマンド

```bash
anna plugin list               # すべてのプラグインとステータスをリスト表示
anna plugin add <path>         # plugin.jsonを含むディレクトリからプラグインをインストール
anna plugin remove <name>      # インストール済みプラグインを削除（エイリアス：rm）
anna plugin enable <name>      # プラグインを有効化
anna plugin disable <name>     # プラグインを無効化
anna plugin config <name>      # プラグイン設定の表示/設定
```

`add`コマンドはプラグインディレクトリを`~/.anna/plugins/installed/`にコピーし、データベースに登録します。`remove`コマンドはエントリとインストールされたファイルを削除します。

## ユーザーインストールプラグイン

ユーザープラグインは`~/.anna/plugins/installed/<name>/`にインストールされます。各ディレクトリには`plugin.json`マニフェストとエントリポイントバイナリまたはスクリプトが必要です。

プラグインをインストールするには：

```bash
anna plugin add /path/to/my-plugin
```

これによりプラグインがインストールディレクトリにコピーされ、データベースに登録・有効化されます。プラグインは次回のanna起動時に読み込まれます。

## プロトコル

プラグインはstdin/stdoutを通じてJSONベースのプロトコルでannaと通信します：

1. **ホストがリクエストを送信**（stdinのJSON行）：`{"method": "execute", "params": {"tool": "my_tool", "input": {...}}}`
2. **プラグインがレスポンスを送信**（stdoutのJSON行）：`{"result": "ツール出力テキスト"}`または`{"error": "エラーメッセージ"}`

プラグインのstderrはannaの構造化ログに転送されます。

## セキュリティモデル

- プラグインはプロセス外で実行されます。クラッシュしてもannaメインデーモンはクラッシュしません。
- サブプロセスプラグインは監視され、適切なタイミングで再起動されます。
- ツールプラグインはパス検証により許可されたディレクトリに制限されます。
- プラグインのstderrはキャプチャされ、annaの構造化ログに転送されます。
