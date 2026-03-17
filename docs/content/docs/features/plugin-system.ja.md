---
title: プラグインシステム
---

## 概要

Annaは、カスタムツールとライフサイクルフックでアシスタントを拡張するJavaScriptプラグインをサポートしています。プラグインは組み込みの[QuickJS](https://bellard.org/quickjs/)ランタイム内で実行されます。外部のNode.jsやnpmは必要ありません。

プラグインは単一の`.js`ファイルで、`anna`ホストオブジェクトを受け取り、それを使用してツールの登録、ライフサイクルイベントへの登録、ホストAPIへのアクセスを行います。

## クイックスタート

1. プラグインファイルを作成します。

```js
// hello.js
anna.registerTool({
  name: 'hello',
  description: 'Say hello to someone.',
  parameters: {
    type: 'object',
    properties: {
      name: { type: 'string', description: 'Name to greet' },
    },
  },
  execute: function (args) {
    return 'Hello, ' + (args.name || 'world') + '!';
  },
});
```

2. annaに追加します。

```bash
anna plugin add hello.js
```

3. annaを再起動します。`hello`ツールがチャットで利用できるようになります。

## CLIコマンド

```bash
anna plugin list                         # 設定されたプラグインをリスト表示
anna plugin add <path>                   # プラグインを追加
anna plugin add <path> --config key=val  # 設定値を指定して追加（繰り返し可能）
anna plugin remove <name|path>           # プラグインを削除（エイリアス: rm）
```

`add`コマンドは、プラグインエントリをデータベースの`settings`テーブル（`"plugins"`キー配下）に書き込みます。`remove`コマンドは、プラグイン名（`.js`拡張子を除いたファイル名）またはフルパスのいずれかを受け付けます。両コマンドとも`settings`テーブルを直接更新します。

## 設定

プラグインは`settings`テーブルの`"plugins"`キー配下にJSON配列として保存されます。各エントリには以下が含まれます。

| フィールド | 型     | 説明                                                                  |
| ---------- | ------ | --------------------------------------------------------------------- |
| `path`     | string | `.js`ファイルへのパス。`~`展開をサポート。                            |
| `config`   | map    | プラグインに`anna.config`として渡されるオプションのキーバリューペア。 |

settingsテーブルに保存されるJSON構造の例:

```json
[
  { "path": "~/plugins/hello.js" },
  {
    "path": "/absolute/path/to/notify.js",
    "config": {
      "webhook_url": "https://example.com/hook",
      "verbose": "true"
    }
  }
]
```

`anna plugin add`および`anna plugin remove` CLIコマンドを使用してこのリストを管理するか、管理パネルから編集してください。

## プラグインの作成

プラグインファイルは、`anna`ホストオブジェクトを受け取るIIFE内で実行されます。すべての登録はロード時に行われます。モジュールシステムや`require`はありません。

### ツールの登録

```js
anna.registerTool({
  name: 'my_tool', // 一意である必要があり、組み込みツールと競合不可
  description: 'What the tool does.',
  parameters: {
    // 入力のためのJSON Schema
    type: 'object',
    properties: {
      query: { type: 'string', description: 'Search query' },
    },
  },
  execute: function (args) {
    // argsはスキーマに一致するプレーンオブジェクト
    // 文字列結果を返す
    return 'result: ' + args.query;
  },
});
```

- **name**（必須）: 一意のツール名。組み込みツールや他のプラグインのツールと名前が競合する場合、登録は失敗します。
- **description**: LLMにツールをいつ呼び出すかを判断させるために表示されます。
- **parameters**: 入力を記述するJSON Schemaオブジェクト。引数生成のためにLLMに渡されます。
- **execute**（必須）: パースされた引数を受け取り、文字列を返す関数。

### ライフサイクルフック

`anna.on(event, handler)`でライフサイクルイベントを購読します。

```js
anna.on('session_start', function (event) {
  anna.log('info', 'Session started: ' + event.sessionId);
});

anna.on('before_tool_call', function (event) {
  anna.log('info', 'Calling: ' + event.toolName);
  // ツール呼び出しをブロックするには空でない文字列を返す
  if (event.toolName === 'dangerous_tool') {
    return 'blocked by policy';
  }
});

anna.on('after_tool_call', function (event) {
  anna.log('info', event.toolName + ' finished, error=' + event.isError);
});

anna.on('session_end', function (event) {
  anna.log('info', 'Session ended: ' + event.sessionId);
});
```

| イベント           | データフィールド        | ブロック                               |
| ------------------ | ----------------------- | -------------------------------------- |
| `session_start`    | `sessionId`, `channel`  | 不可                                   |
| `session_end`      | `sessionId`, `channel`  | 不可                                   |
| `before_tool_call` | `toolName`, `arguments` | 可 -- 空でない文字列を返すとキャンセル |
| `after_tool_call`  | `toolName`, `isError`   | 不可                                   |

`before_tool_call`の場合、空でない文字列を返した最初のフックが実行を停止し、ツール呼び出しはキャンセルされます。その他のイベントはすべてfire-and-forgetです。

### ホストAPI

`anna`オブジェクトは以下のAPIを提供します。

#### `anna.config`

プラグインのsettingsエントリからの設定マップ。`anna.config.key_name`で値にアクセスします。

#### `anna.log(level, message)`

annaの構造化ロガーに書き込みます。レベル: `"debug"`, `"info"`, `"warn"`, `"error"`。

```js
anna.log('info', 'plugin loaded');
anna.log('error', 'something went wrong');
```

#### `anna.readFile(path)` / `anna.writeFile(path, content)`

ファイルを読み書きします。パスはプラグインディレクトリからの相対パスとして解決されます。絶対パスも受け付けます。

**サンドボックス化**: ファイルアクセスはプラグインの親ディレクトリと`~/.anna/workspaces/`に制限されます。これらのディレクトリ外のパスへのアクセス試行は拒否されます。

```js
var data = anna.readFile('data.json'); // プラグインディレクトリからの相対パス
anna.writeFile('output.txt', 'hello'); // プラグインディレクトリからの相対パス
var soul = anna.readFile(anna.config.soul); // 設定からの絶対パス
```

#### `anna.fetch(url, options?)`

安全性の制約を持つHTTPクライアント。`{ status, body }`を返します。

```js
var resp = anna.fetch('https://api.example.com/data', {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ query: 'hello' }),
  timeout: 5000, // ミリ秒（デフォルト: 30秒、最大: 60秒）
});

if (resp.status === 200) {
  var data = JSON.parse(resp.body);
}
```

**制約**:

- `http`および`https`スキームのみ許可されます。
- プライベート/内部IP（ループバック、RFC 1918、リンクローカル）へのリクエストはブロックされます（SSRF保護）。
- レスポンスボディは1MBに制限されます。
- デフォルトのタイムアウトは30秒、最大は60秒です。

## セキュリティモデル

プラグインは、以下の制限を持つサンドボックス化されたQuickJSランタイム内で実行されます。

- **Node.js APIなし**: `require`、`process`、`fs`、`child_process`はありません。上記のホストAPIのみが利用可能です。
- **ファイルアクセス**: プラグインディレクトリと`~/.anna/workspaces/`にサンドボックス化されます。
- **ネットワークアクセス**: HTTP(S)のみ、SSRF安全（プライベートIPがブロックされ、DNSリバインディングが防止される）。
- **並行性**: QuickJSはシングルスレッドなので、すべてのJS呼び出しはミューテックスでシリアライズされます。
- **ツール名の分離**: プラグインツールは組み込みツールをシャドウできません。

## 例

### ライフサイクルロガー

すべてのライフサイクルイベントをログに記録します。デバッグに便利です。

```js
// lifecycle-logger.js
anna.on('session_start', function (event) {
  anna.log('info', '[lifecycle] session_start id=' + event.sessionId);
});

anna.on('session_end', function (event) {
  anna.log('info', '[lifecycle] session_end id=' + event.sessionId);
});

anna.on('before_tool_call', function (event) {
  anna.log('info', '[lifecycle] before_tool_call tool=' + event.toolName);
});

anna.on('after_tool_call', function (event) {
  var status = event.isError ? 'ERROR' : 'OK';
  anna.log('info', '[lifecycle] after_tool_call tool=' + event.toolName + ' ' + status);
});
```

### ターミナル通知

エラー時にターミナルベルを鳴らし、`notify`ツールを提供します。

```js
// notify.js
anna.on('after_tool_call', function (event) {
  if (event.isError) {
    anna.log('warn', '\x07[notify] tool error: ' + event.toolName);
  }
});

anna.registerTool({
  name: 'notify',
  description: 'Sends a terminal bell notification with a custom message.',
  parameters: {
    type: 'object',
    properties: {
      message: { type: 'string', description: 'Notification message' },
    },
  },
  execute: function (args) {
    var msg = args.message || 'notification';
    anna.log('info', '\x07[notify] ' + msg);
    return 'notified: ' + msg;
  },
});
```

### Webhookフォワーダー

設定とfetchを使用してツール呼び出し結果を外部webhookにポストします。インライン設定でプラグインを追加します。

```bash
anna plugin add ~/plugins/webhook.js --config url=https://hooks.example.com/anna
```

```js
// webhook.js
anna.on('after_tool_call', function (event) {
  if (anna.config.url) {
    anna.fetch(anna.config.url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        tool: event.toolName,
        error: event.isError,
      }),
    });
  }
});
```

その他の例は[`examples/plugins/`](https://github.com/vaayne/anna/tree/main/examples/plugins)ディレクトリにあります。
