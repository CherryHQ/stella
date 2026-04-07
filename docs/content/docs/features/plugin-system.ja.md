---
title: プラグインシステム
---

## 概要

Annaはコンパイル組み込みのプラグインモデルを採用しています。すべてのプラグインはannaバイナリに直接コンパイルされています。サブプロセスプロトコル、別プロセス、サードパーティプラグインのインストールはありません。プラグインは`plugins/`以下で`init()`による自己登録と標準インターフェースの実装を行うGoパッケージです。

4種類のプラグイン：

- **ツールプラグイン**はLLMエージェントが呼び出せるツールを提供します（例：`mcp`、`webfetch`）。
- **チャネルプラグイン**はメッセージングプラットフォーム統合を提供します（例：`telegram`、`qq`、`feishu`、`weixin`）。
- **フックプラグイン**はエンジンのライフサイクルイベントをインターセプトします（例：ツール呼び出し前後、LLM呼び出し前後）。
- **プロバイダープラグイン**はLLM APIアダプターを提供します（例：`anthropic`、`openai`、`openai-response`）。

注意：コアツール（`read`、`bash`、`edit`、`write`）は常に有効で、プラグインではありません。

## ビルトインプラグイン

Annaには9つのビルトインプラグインがあります：

| 種類     | 名前            | 説明                                          |
| -------- | --------------- | --------------------------------------------- |
| tool     | mcp             | 設定済みMCPサーバーへ接続し、MCPツールをプロキシする |
| tool     | webfetch        | Webページ取得                                 |
| channel  | telegram        | Telegramボット                                |
| channel  | qq              | QQボット                                      |
| channel  | feishu          | 飛書（Lark）ボット                            |
| channel  | weixin          | WeChatボット（iLink経由）                     |
| hook     | rtk             | リクエスト追跡とコストロギング                |
| provider | anthropic       | Anthropic Messages API（Claudeモデル）        |
| provider | openai          | OpenAI Chat Completions API（GPTモデル）      |
| provider | openai-response | OpenAI Responses API（互換サービス）          |

## プラグインアーキテクチャ

すべてのプラグイン種類は同じパターンに従います：

1. 各プラグインは`plugins/{kind}/{name}/`以下のGoパッケージです
2. パッケージの`init()`関数が種類固有のレジストリの`Register()`メソッドを呼び出します
3. `plugins/all.go`のブランクインポートが起動時に登録をトリガーします
4. レジストリの`BuildEnabled()`（プロバイダーは`BuildAll()`）が実行時にアクティブなプラグインをインスタンス化します

```
plugins/
├── all.go                          # ブランクインポートでinit()登録をトリガー
├── tools/
│   ├── registry.go                 # ツールプラグインレジストリ
│   ├── mcp/                        # ツール：MCPプロキシとサーバー管理
│   └── webfetch/                   # ツール：Webページフェッチャー
├── channels/
│   ├── telegram/                   # チャネル：Telegramボット
│   ├── qq/                         # チャネル：QQボット
│   ├── feishu/                     # チャネル：飛書ボット
│   └── weixin/                     # チャネル：WeChatボット
├── hooks/
│   ├── registry.go                 # フックプラグインレジストリ
│   └── rtk/                        # フック：リクエスト追跡
└── providers/
    ├── registry.go                 # プロバイダープラグインレジストリ
    ├── anthropic/                  # プロバイダー：Anthropic API
    ├── openai/                     # プロバイダー：OpenAI Chat Completions
    └── openai-response/            # プロバイダー：OpenAI Responses API
```

### 新しいプラグインの追加

新しいプラグインを追加するには、適切な`plugins/{kind}/`ディレクトリの下に`init()`関数を含むパッケージを作成し、種類のレジストリに登録します。次に`plugins/all.go`にブランクインポートを追加します。他の接続コードは不要です。

例——新しいプロバイダーの追加：

```go
// plugins/providers/gemini/client.go
package gemini

import (
    pluginproviders "github.com/vaayne/anna/plugins/providers"
    "github.com/vaayne/anna/internal/ai"
)

func init() {
    pluginproviders.Register("gemini", pluginproviders.Registration{
        Meta: pluginproviders.ProviderMeta{
            Name:       "Google Gemini",
            DefaultURL: "https://generativelanguage.googleapis.com",
        },
        Factory: func(cfg pluginproviders.ProviderConfig) (providers.ProviderAdapter, error) {
            return New(Config{APIKey: cfg.APIKey, BaseURL: cfg.BaseURL}), nil
        },
    })
}
```

## ストレージ

プラグインの状態はデータベースの`settings_plugins`テーブルに保存されます。各プラグインには以下が含まれます：

| フィールド | 型         | 説明                                              |
| ---------- | ---------- | ------------------------------------------------- |
| `id`       | string     | プラグインID（`種類/名前`、例：`tool/webfetch`）  |
| `kind`     | string     | `tool`、`channel`、`hook`、または`provider`       |
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

チャネルプラグイン、プロバイダープラグイン、そして組み込みの`tool/mcp`プラグインは管理パネル（`anna --open`）で設定します。管理パネルは`settings_plugins`テーブルに書き込み、トークン、キー、プラグイン固有設定を管理するUIを提供します。MCPプラグインはサーバー定義を`settings_plugins.config`にJSONで保存し、プラグインページで複数サーバー/複数トランスポートをフォーム編集できます。
