---
title: 插件系统
---

## 概述

Anna 支持 JavaScript 插件来扩展助手的自定义工具和生命周期钩子。插件运行在内嵌的 [QuickJS](https://bellard.org/quickjs/) 运行时中——无需外部 Node.js 或 npm。

插件是一个单独的 `.js` 文件,接收一个 `anna` 宿主对象并使用它来注册工具、订阅生命周期事件和访问宿主 API。

## 快速开始

1. 创建插件文件:

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

2. 将它添加到 anna:

```bash
anna plugin add hello.js
```

3. 重启 anna。现在 `hello` 工具在聊天中可用了。

## CLI 命令

```bash
anna plugin list                         # 列出已配置的插件
anna plugin add <path>                   # 添加插件
anna plugin add <path> --config key=val  # 添加时附带配置值(可重复)
anna plugin remove <name|path>           # 删除插件(别名: rm)
```

`add` 命令将插件条目写入数据库的 `settings` 表(在 `"plugins"` 键下)。`remove` 命令接受插件名称(不含 `.js` 的文件名)或完整路径。两个命令都直接更新 `settings` 表。

## 配置

插件存储在 `settings` 表中,键为 `"plugins"`,值为 JSON 数组。每个条目包含:

| 字段     | 类型   | 描述                                         |
| -------- | ------ | -------------------------------------------- |
| `path`   | string | `.js` 文件的路径。支持 `~` 扩展。            |
| `config` | map    | 可选的键值对,作为 `anna.config` 传递给插件。 |

存储在 settings 表中的 JSON 结构示例:

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

使用 `anna plugin add` 和 `anna plugin remove` CLI 命令来管理这个列表,或通过管理面板编辑。

## 编写插件

插件文件在接收 `anna` 宿主对象的 IIFE 中执行。所有注册都在加载时发生——没有模块系统或 `require`。

### 注册工具

```js
anna.registerTool({
  name: 'my_tool', // 必须唯一,不能与内置工具冲突
  description: 'What the tool does.',
  parameters: {
    // 输入的 JSON Schema
    type: 'object',
    properties: {
      query: { type: 'string', description: 'Search query' },
    },
  },
  execute: function (args) {
    // args 是匹配 schema 的普通对象
    // 返回字符串结果
    return 'result: ' + args.query;
  },
});
```

- **name** (必需): 唯一的工具名称。如果名称与内置工具或其他插件的工具冲突,注册将失败。
- **description**: 向 LLM 展示以决定何时调用工具。
- **parameters**: 描述输入的 JSON Schema 对象。传递给 LLM 用于参数生成。
- **execute** (必需): 接收解析后的参数并返回字符串的函数。

### 生命周期钩子

使用 `anna.on(event, handler)` 订阅生命周期事件:

```js
anna.on('session_start', function (event) {
  anna.log('info', 'Session started: ' + event.sessionId);
});

anna.on('before_tool_call', function (event) {
  anna.log('info', 'Calling: ' + event.toolName);
  // 返回非空字符串以阻止工具调用
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

| 事件               | 数据字段                | 阻塞                         |
| ------------------ | ----------------------- | ---------------------------- |
| `session_start`    | `sessionId`, `channel`  | 否                           |
| `session_end`      | `sessionId`, `channel`  | 否                           |
| `before_tool_call` | `toolName`, `arguments` | 是——返回非空字符串以取消调用 |
| `after_tool_call`  | `toolName`, `isError`   | 否                           |

对于 `before_tool_call`,第一个返回非空字符串的钩子会停止执行,工具调用将被取消。所有其他事件都是即发即弃。

### 宿主 API

`anna` 对象提供以下 API:

#### `anna.config`

来自插件 settings 条目的配置映射。使用 `anna.config.key_name` 访问值。

#### `anna.log(level, message)`

写入 anna 的结构化日志。级别: `"debug"`, `"info"`, `"warn"`, `"error"`。

```js
anna.log('info', 'plugin loaded');
anna.log('error', 'something went wrong');
```

#### `anna.readFile(path)` / `anna.writeFile(path, content)`

读写文件。路径相对于插件目录解析。也接受绝对路径。

**沙箱化**: 文件访问限制在插件的父目录和 `~/.anna/workspaces/` 内。尝试访问这些目录之外的路径将被拒绝。

```js
var data = anna.readFile('data.json'); // 相对于插件目录
anna.writeFile('output.txt', 'hello'); // 相对于插件目录
var soul = anna.readFile(anna.config.soul); // 来自配置的绝对路径
```

#### `anna.fetch(url, options?)`

带有安全约束的 HTTP 客户端。返回 `{ status, body }`。

```js
var resp = anna.fetch('https://api.example.com/data', {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ query: 'hello' }),
  timeout: 5000, // 毫秒(默认: 30秒,最大: 60秒)
});

if (resp.status === 200) {
  var data = JSON.parse(resp.body);
}
```

**约束**:

- 只允许 `http` 和 `https` 协议。
- 阻止对私有/内部 IP(回环地址、RFC 1918、本地链接)的请求(SSRF 防护)。
- 响应体限制为 1 MB。
- 默认超时 30 秒,最大 60 秒。

## 安全模型

插件在沙箱化的 QuickJS 运行时中运行,具有以下限制:

- **无 Node.js API**: 没有 `require`, `process`, `fs` 或 `child_process`。只有上面列出的宿主 API 可用。
- **文件访问**: 沙箱化到插件目录和 `~/.anna/workspaces/`。
- **网络访问**: 仅 HTTP(S),SSRF 安全(阻止私有 IP,防止 DNS 重绑定)。
- **并发**: 所有 JS 调用使用互斥锁序列化,因为 QuickJS 是单线程的。
- **工具名称隔离**: 插件工具不能覆盖内置工具。

## 示例

### 生命周期日志器

记录每个生命周期事件——对调试很有用:

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

### 终端通知

在错误时发送终端响铃并提供 `notify` 工具:

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

### Webhook 转发器

使用配置和 fetch 将工具调用结果发送到外部 webhook。使用内联配置添加插件:

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

更多示例请参见 [`examples/plugins/`](https://github.com/vaayne/anna/tree/main/examples/plugins) 目录。
