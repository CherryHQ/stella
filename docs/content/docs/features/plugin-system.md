---
title: Plugin System
---

## Overview

Anna supports JavaScript plugins that extend the assistant with custom tools and lifecycle hooks. Plugins run inside an embedded [QuickJS](https://bellard.org/quickjs/) runtime -- no external Node.js or npm required.

A plugin is a single `.js` file that receives an `anna` host object and uses it to register tools, subscribe to lifecycle events, and access host APIs.

## Quick Start

1. Create a plugin file:

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

2. Add it to anna:

```bash
anna plugin add hello.js
```

3. Restart anna. The `hello` tool is now available in chat.

## CLI Commands

```bash
anna plugin list                         # List configured plugins
anna plugin add <path>                   # Add a plugin
anna plugin add <path> --config key=val  # Add with config values (repeatable)
anna plugin remove <name|path>           # Remove a plugin (alias: rm)
```

The `add` command writes the plugin entry into the `settings` table in the database (under the `"plugins"` key). The `remove` command accepts either the plugin name (filename without `.js`) or the full path. Both commands update the `settings` table directly.

## Configuration

Plugins are stored in the `settings` table under the key `"plugins"` as a JSON array. Each entry has:

| Field    | Type   | Description                                                     |
| -------- | ------ | --------------------------------------------------------------- |
| `path`   | string | Path to the `.js` file. Supports `~` expansion.                 |
| `config` | map    | Optional key-value pairs passed to the plugin as `anna.config`. |

Example JSON structure stored in the settings table:

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

Use the `anna plugin add` and `anna plugin remove` CLI commands to manage this list, or edit it through the admin panel.

## Writing Plugins

A plugin file is executed inside an IIFE that receives the `anna` host object. All registration happens at load time -- there is no module system or `require`.

### Registering Tools

```js
anna.registerTool({
  name: 'my_tool', // Must be unique, cannot conflict with built-in tools
  description: 'What the tool does.',
  parameters: {
    // JSON Schema for the input
    type: 'object',
    properties: {
      query: { type: 'string', description: 'Search query' },
    },
  },
  execute: function (args) {
    // args is a plain object matching the schema
    // Return a string result
    return 'result: ' + args.query;
  },
});
```

- **name** (required): Unique tool name. Registration fails if the name conflicts with a built-in tool or another plugin's tool.
- **description**: Shown to the LLM to decide when to call the tool.
- **parameters**: JSON Schema object describing the input. Passed to the LLM for argument generation.
- **execute** (required): Function that receives the parsed arguments and returns a string.

### Lifecycle Hooks

Subscribe to lifecycle events with `anna.on(event, handler)`:

```js
anna.on('session_start', function (event) {
  anna.log('info', 'Session started: ' + event.sessionId);
});

anna.on('before_tool_call', function (event) {
  anna.log('info', 'Calling: ' + event.toolName);
  // Return a non-empty string to BLOCK the tool call
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

| Event              | Data Fields             | Blocking                                   |
| ------------------ | ----------------------- | ------------------------------------------ |
| `session_start`    | `sessionId`, `channel`  | No                                         |
| `session_end`      | `sessionId`, `channel`  | No                                         |
| `before_tool_call` | `toolName`, `arguments` | Yes -- return a non-empty string to cancel |
| `after_tool_call`  | `toolName`, `isError`   | No                                         |

For `before_tool_call`, the first hook that returns a non-empty string stops execution and the tool call is cancelled. All other events are fire-and-forget.

### Host APIs

The `anna` object provides these APIs:

#### `anna.config`

The config map from the plugin's settings entry. Access values with `anna.config.key_name`.

#### `anna.log(level, message)`

Write to anna's structured logger. Levels: `"debug"`, `"info"`, `"warn"`, `"error"`.

```js
anna.log('info', 'plugin loaded');
anna.log('error', 'something went wrong');
```

#### `anna.readFile(path)` / `anna.writeFile(path, content)`

Read and write files. Paths are resolved relative to the plugin directory. Absolute paths are also accepted.

**Sandboxed**: file access is restricted to the plugin's parent directory and `~/.anna/workspaces/`. Attempts to access paths outside these directories are denied.

```js
var data = anna.readFile('data.json'); // relative to plugin dir
anna.writeFile('output.txt', 'hello'); // relative to plugin dir
var soul = anna.readFile(anna.config.soul); // absolute path from config
```

#### `anna.fetch(url, options?)`

HTTP client with safety constraints. Returns `{ status, body }`.

```js
var resp = anna.fetch('https://api.example.com/data', {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ query: 'hello' }),
  timeout: 5000, // milliseconds (default: 30s, max: 60s)
});

if (resp.status === 200) {
  var data = JSON.parse(resp.body);
}
```

**Constraints**:

- Only `http` and `https` schemes are allowed.
- Requests to private/internal IPs (loopback, RFC 1918, link-local) are blocked (SSRF protection).
- Response body is capped at 1 MB.
- Default timeout is 30 seconds, maximum is 60 seconds.

## Security Model

Plugins run in a sandboxed QuickJS runtime with these restrictions:

- **No Node.js APIs**: No `require`, `process`, `fs`, or `child_process`. Only the host APIs listed above are available.
- **File access**: Sandboxed to the plugin directory and `~/.anna/workspaces/`.
- **Network access**: HTTP(S) only, SSRF-safe (private IPs blocked, DNS rebinding prevented).
- **Concurrency**: All JS calls are serialized with a mutex since QuickJS is single-threaded.
- **Tool name isolation**: Plugin tools cannot shadow built-in tools.

## Examples

### Lifecycle Logger

Logs every lifecycle event -- useful for debugging:

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

### Terminal Notification

Sends a terminal bell on errors and provides a `notify` tool:

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

### Webhook Forwarder

Posts tool call results to an external webhook using config and fetch. Add the plugin with inline config:

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

More examples in the [`examples/plugins/`](https://github.com/vaayne/anna/tree/main/examples/plugins) directory.
