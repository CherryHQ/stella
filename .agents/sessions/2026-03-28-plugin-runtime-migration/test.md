# Plugin Runtime Manual Test Plan

## Prerequisites

```bash
mise run build
```

Binaries: `bin/anna`, `bin/anna-plugin`

---

## 1. Tool Plugins

Each tool runs as a subprocess speaking NDJSON on stdin/stdout.
Pipe requests, check responses, `2>/dev/null` hides debug stderr.

### 1.1 Read

```bash
echo '{"id":"1","type":"request","method":"handshake","params":{"protocol_version":"anna-plugin/v1"}}
{"id":"2","type":"request","method":"call_tool","params":{"name":"read","arguments":{"file_path":"README.md","limit":3}}}
{"id":"3","type":"request","method":"shutdown"}' \
  | bin/anna-plugin tool read --work-dir . 2>/dev/null | jq .
```

**Expect:** 3 responses — handshake with tool schema, call_tool with first 3 lines of README, shutdown ack.

### 1.2 Bash

```bash
echo '{"id":"1","type":"request","method":"handshake","params":{"protocol_version":"anna-plugin/v1"}}
{"id":"2","type":"request","method":"call_tool","params":{"name":"bash","arguments":{"command":"echo hello && pwd"}}}
{"id":"3","type":"request","method":"shutdown"}' \
  | bin/anna-plugin tool bash --work-dir /tmp 2>/dev/null | jq .
```

**Expect:** call_tool output contains `hello` and `/private/tmp` (or `/tmp`).

### 1.3 Write

```bash
echo '{"id":"1","type":"request","method":"handshake","params":{"protocol_version":"anna-plugin/v1"}}
{"id":"2","type":"request","method":"call_tool","params":{"name":"write","arguments":{"file_path":"/tmp/plugin-test.txt","content":"written by plugin"}}}
{"id":"3","type":"request","method":"shutdown"}' \
  | bin/anna-plugin tool write --work-dir /tmp 2>/dev/null | jq .

cat /tmp/plugin-test.txt
```

**Expect:** file contains `written by plugin`.

### 1.4 Edit

```bash
# Requires /tmp/plugin-test.txt from 1.3
echo '{"id":"1","type":"request","method":"handshake","params":{"protocol_version":"anna-plugin/v1"}}
{"id":"2","type":"request","method":"call_tool","params":{"name":"edit","arguments":{"file_path":"/tmp/plugin-test.txt","old_string":"written","new_string":"edited"}}}
{"id":"3","type":"request","method":"shutdown"}' \
  | bin/anna-plugin tool edit --work-dir /tmp 2>/dev/null | jq .

cat /tmp/plugin-test.txt
```

**Expect:** file now contains `edited by plugin`.

### 1.5 Webfetch

```bash
echo '{"id":"1","type":"request","method":"handshake","params":{"protocol_version":"anna-plugin/v1"}}
{"id":"2","type":"request","method":"call_tool","params":{"name":"webfetch","arguments":{"url":"https://example.com","format":"text"}}}
{"id":"3","type":"request","method":"shutdown"}' \
  | bin/anna-plugin tool webfetch --work-dir /tmp 2>/dev/null | jq .
```

**Expect:** call_tool output contains `Example Domain`.

### 1.6 Health check

```bash
echo '{"id":"1","type":"request","method":"handshake","params":{"protocol_version":"anna-plugin/v1"}}
{"id":"2","type":"request","method":"health"}
{"id":"3","type":"request","method":"shutdown"}' \
  | bin/anna-plugin tool read --work-dir . 2>/dev/null | jq .
```

**Expect:** health response `{"ok": true}`.

### 1.7 Unknown method

```bash
echo '{"id":"1","type":"request","method":"handshake","params":{"protocol_version":"anna-plugin/v1"}}
{"id":"2","type":"request","method":"bogus"}
{"id":"3","type":"request","method":"shutdown"}' \
  | bin/anna-plugin tool read --work-dir . 2>/dev/null | jq .
```

**Expect:** response 2 has `"error": {"code": "unknown_method", ...}`.

---

## 2. Runtime Binding CLI

### 2.1 List bindings

```bash
bin/anna plugin runtime list
```

**Expect:** table showing all 5 tool slots (read, bash, edit, write, webfetch) and 4 channel slots (telegram, qq, feishu, weixin), all bound to `bundled` source.

### 2.2 Bind a third-party tool

Create a fake plugin:

```bash
mkdir -p /tmp/test-plugin
cat > /tmp/test-plugin/plugin.sh << 'EOF'
#!/bin/sh
while IFS= read -r line; do
  id=$(echo "$line" | jq -r .id)
  method=$(echo "$line" | jq -r .method)
  case "$method" in
    handshake) printf '{"id":"%s","type":"response","result":{"protocol_version":"anna-plugin/v1","name":"custom-read","version":"0.1.0","kind":"tool","capabilities":["tool.call","health.check","shutdown.graceful"],"tool":{"name":"read","description":"custom read","input_schema":{"type":"object"}}}}\n' "$id" ;;
    health)    printf '{"id":"%s","type":"response","result":{"ok":true}}\n' "$id" ;;
    call_tool) printf '{"id":"%s","type":"response","result":{"output":"custom read output!"}}\n' "$id" ;;
    shutdown)  printf '{"id":"%s","type":"response","result":{}}\n' "$id"; exit 0 ;;
  esac
done
EOF
chmod +x /tmp/test-plugin/plugin.sh
```

Test it standalone first:

```bash
echo '{"id":"1","type":"request","method":"handshake","params":{"protocol_version":"anna-plugin/v1"}}
{"id":"2","type":"request","method":"call_tool","params":{"name":"read","arguments":{}}}
{"id":"3","type":"request","method":"shutdown"}' \
  | /tmp/test-plugin/plugin.sh | jq .
```

**Expect:** `"output": "custom read output!"`.

Install it as a plugin:

```bash
PLUGIN_DIR="$HOME/.anna/plugins/custom-read/0.1.0"
mkdir -p "$PLUGIN_DIR"
cp /tmp/test-plugin/plugin.sh "$PLUGIN_DIR/"
cat > "$PLUGIN_DIR/plugin.json" << 'EOF'
{
  "name": "custom-read",
  "version": "0.1.0",
  "kind": "tool",
  "protocol_version": "anna-plugin/v1",
  "entrypoint": "plugin.sh",
  "tool": {
    "name": "read",
    "description": "custom read replacement",
    "input_schema": {"type": "object"}
  },
  "capabilities": ["tool.call", "health.check", "shutdown.graceful"]
}
EOF
```

Bind and verify:

```bash
bin/anna plugin runtime bind tool read tool/custom-read
bin/anna plugin runtime list
```

**Expect:** read slot now shows `tool/custom-read` with source `installed`.

Reset back to default:

```bash
bin/anna plugin runtime bind tool read --default
bin/anna plugin runtime list
```

**Expect:** read slot back to `tool/read` bundled.

### 2.3 Bind errors

```bash
# Unknown slot
bin/anna plugin runtime bind tool bogus tool/custom-read
# Expect: error "unknown tool slot"

# Missing plugin
bin/anna plugin runtime bind tool read tool/nonexistent
# Expect: error "not found"

# Wrong kind
bin/anna plugin runtime bind channel telegram tool/custom-read
# Expect: error about kind mismatch
```

---

## 3. Interactive Mode

Run a tool plugin interactively — type one JSON line at a time, see each response immediately:

```bash
bin/anna-plugin tool bash --work-dir /tmp
```

Then paste line by line:

```
{"id":"1","type":"request","method":"handshake","params":{"protocol_version":"anna-plugin/v1"}}
```

Wait for response, then:

```
{"id":"2","type":"request","method":"call_tool","params":{"name":"bash","arguments":{"command":"date"}}}
```

Wait for response, then Ctrl+D or:

```
{"id":"3","type":"request","method":"shutdown"}
```

**Expect:** each response appears after each input line.

---

## 4. End-to-End: Chat with Plugin Tools

Start anna in CLI chat mode and confirm tools work through the full agent loop:

```bash
bin/anna chat
```

Ask it to read a file, run a command, etc. The tools are now routed through subprocess plugins — verify the responses are normal.

---

## 5. Cleanup

```bash
rm -f /tmp/plugin-test.txt
rm -rf /tmp/test-plugin
rm -rf "$HOME/.anna/plugins/custom-read"
bin/anna plugin runtime bind tool read --default 2>/dev/null
```
