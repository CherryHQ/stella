---
title: Plugin System
---

## Overview

Anna supports a dual plugin system that lets you extend the assistant with custom tools and lifecycle hooks. There are two plugin types:

- **JS extensions** -- sandboxed JavaScript files loaded at runtime, no compilation required
- **Go plugins** -- Go packages compiled into a custom anna binary for full-power extensions

Both types are configured in `~/.anna/config.yaml` under the `plugins:` key.

## Configuration

Each plugin entry specifies a `path` and an optional `config` map that is passed to the plugin at load time:

```yaml
plugins:
  - path: ~/.anna/plugins/weather.js
    config:
      api_key: "xxx"
  - path: ~/.anna/plugins/github-tools
    config:
      token: "yyy"
```

- **JS extensions**: `path` points to a `.js` file
- **Go plugins**: `path` points to a directory containing Go source

## JS Extensions

JS extensions are single `.js` files that run in a sandboxed QuickJS runtime. They interact with anna through the `anna` host object, which provides the following APIs:

### Registering tools

```js
anna.registerTool({
  name: "weather_lookup",
  description: "Look up current weather for a city",
  parameters: {
    type: "object",
    properties: {
      city: { type: "string", description: "City name" }
    },
    required: ["city"]
  }
}, function(params) {
  var resp = anna.fetch("https://api.weather.example.com/v1/current?q=" + params.city, {
    headers: { "Authorization": "Bearer " + anna.config.api_key }
  });
  return JSON.parse(resp.body);
});
```

### Lifecycle hooks

```js
anna.on("session:start", function(event) {
  anna.log("Session started: " + event.session_id);
});

anna.on("session:end", function(event) {
  anna.log("Session ended: " + event.session_id);
});

anna.on("tool:before", function(event) {
  anna.log("About to call tool: " + event.tool_name);
});

anna.on("tool:after", function(event) {
  anna.log("Tool completed: " + event.tool_name);
});
```

### Host APIs

| Function | Description |
|----------|-------------|
| `anna.registerTool(schema, handler)` | Register a new tool with JSON Schema parameters |
| `anna.on(event, handler)` | Subscribe to a lifecycle event |
| `anna.log(message)` | Write to anna's log output |
| `anna.readFile(path)` | Read a file (scoped to workspace) |
| `anna.writeFile(path, content)` | Write a file (scoped to workspace) |
| `anna.fetch(url, options)` | HTTP request (with size and timeout limits) |
| `anna.config` | The `config` map from the plugin's YAML entry |

## Go Plugins

Go plugins are full Go packages that are compiled into the anna binary. They use an `init()` function to register a factory with the plugin manager:

```go
package myplugin

import "github.com/anthropic/anna/internal/plugin"

func init() {
    plugin.Register("myplugin", Factory)
}

func Factory(cfg map[string]any) (plugin.Plugin, error) {
    return &myPlugin{apiKey: cfg["api_key"].(string)}, nil
}

type myPlugin struct {
    apiKey string
}

// Implement plugin.Plugin interface...
```

After adding or updating a Go plugin, rebuild the binary:

```bash
anna plugin build
```

This produces a custom anna binary with the Go plugin compiled in.

## CLI Commands

```
anna plugin list        # List all configured plugins and their status
anna plugin add <path>  # Add a JS or Go plugin to config
anna plugin remove <n>  # Remove a plugin from config by index
anna plugin build       # Build custom binary with Go plugins compiled in
```

## Security

### JS sandboxing

JS extensions run inside a QuickJS sandbox with the following restrictions:

- **File access**: `readFile` and `writeFile` are scoped to the anna workspace directory. Path traversal outside the workspace is blocked.
- **Network access**: `fetch` enforces response size limits and connection timeouts to prevent resource exhaustion.
- **No system access**: JS plugins cannot execute shell commands, access environment variables, or interact with the operating system directly.
- **Isolated runtime**: Each plugin runs in its own JS context with no shared mutable state between plugins.

### Go plugins

Go plugins are compiled into the binary and run with the same permissions as anna itself. Only install Go plugins from trusted sources.
