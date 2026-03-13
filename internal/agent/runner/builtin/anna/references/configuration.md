# Configuration reference

Config file: `$ANNA_HOME/config.yaml` (`~/.anna/config.yaml` by default).

The easiest way to configure anna is `anna onboard`, which opens a web UI. For manual editing, see below.

## Minimal setup

```yaml
providers:
  anthropic:
    api_key: "sk-..."

provider: anthropic
model: claude-sonnet-4-6
```

Or just: `export ANTHROPIC_API_KEY="sk-..."` and run `anna chat`.

## Full config

```yaml
providers:
  anthropic:
    api_key: "sk-..."
    base_url: ""                   # optional URL override
    models:                        # optional model metadata
      - id: claude-sonnet-4-6
        reasoning: false
        input: ["text", "image"]
        context_window: 200000
        max_tokens: 8192
        cost:
          input: 3.0
          output: 15.0
          cache_read: 0.3
          cache_write: 3.75
  openai:
    api_key: "sk-..."
    base_url: "https://api.openai.com/v1"
  openai-response:                 # OpenAI-compatible APIs (Perplexity, Together.ai)
    api_key: "sk-..."
    base_url: "https://api.example.com/v1"

channels:
  telegram:
    enabled: true                  # enable/disable this channel (default: true)
    enable_notify: false           # allow notify tool to send to this channel (default: false)
    token: "BOT_TOKEN"
    notify_chat: "123456789"
    channel_id: "@my_channel"
    group_mode: "mention"          # mention | always | disabled
    allowed_ids: [136345060]
  qq:
    enabled: true
    enable_notify: false
    app_id: "QQ_BOT_APP_ID"
    app_secret: "QQ_BOT_APP_SECRET"
    group_mode: "mention"
    allowed_ids: []
  feishu:
    enabled: true
    enable_notify: false
    app_id: "FEISHU_APP_ID"
    app_secret: "FEISHU_APP_SECRET"
    encrypt_key: ""
    verification_token: ""
    notify_chat: "oc_xxx"
    group_mode: "mention"
    allowed_ids: []

provider: anthropic
model: claude-sonnet-4-6
model_strong: claude-opus-4-6     # optional tier
model_fast: claude-haiku-4-5      # optional tier
workspace: "~/.anna/workspace"

runner:
  type: go
  system: ""                       # custom system prompt (overrides default)
  idle_timeout: 10                 # minutes before reaping idle runners
  compaction:
    max_tokens: 80000              # auto-compact threshold (-1 = disabled)
    keep_tail: 20                  # recent messages kept after compaction

scheduler:
  enabled: true

heartbeat:
  enabled: false                   # default: false
  every: 10m                       # poll interval
  file: "HEARTBEAT.md"            # relative to workspace unless absolute
```

Heartbeat only runs in `anna gateway`. Each tick uses the fast model for the skip/run decision and the default model for execution.

## Directory layout

All paths are relative to `$ANNA_HOME` (`~/.anna` by default).

| Path | Purpose |
|------|---------|
| `config.yaml` | Static config (user-edited) |
| `workspace/state.yaml` | Runtime state: current provider/model (program-managed) |
| `cache/models.json` | Cached model list (safe to delete) |
| `workspace/SOUL.md` | Agent identity, personality, tone |
| `workspace/USER.md` | User preferences, name, timezone |
| `workspace/memory.db` | SQLite database (message history, summaries, scheduler jobs) |
| `workspace/skills/` | Installed skills |
| `workspace/HEARTBEAT.md` | Heartbeat instructions |

## Environment variables

Priority (highest wins): env vars > state.yaml > config.yaml > defaults.

| Variable | Overrides |
|----------|-----------|
| `ANNA_HOME` | anna home directory (default `~/.anna`) |
| `ANNA_PROVIDER` | `provider` |
| `ANNA_MODEL` | `model` |
| `ANNA_MODEL_STRONG` | `model_strong` |
| `ANNA_MODEL_FAST` | `model_fast` |
| `ANNA_WORKSPACE` | `workspace` |
| `ANNA_RUNNER_IDLE_TIMEOUT` | `runner.idle_timeout` |
| `ANNA_SCHEDULER_ENABLED` | `scheduler.enabled` |
| `ANNA_HEARTBEAT_ENABLED` | `heartbeat.enabled` |
| `ANNA_HEARTBEAT_EVERY` | `heartbeat.every` |
| `ANNA_HEARTBEAT_FILE` | `heartbeat.file` |
| `ANNA_TELEGRAM_ENABLED` | `channels.telegram.enabled` |
| `ANNA_TELEGRAM_ENABLE_NOTIFY` | `channels.telegram.enable_notify` |
| `ANNA_TELEGRAM_TOKEN` | `channels.telegram.token` |
| `ANNA_TELEGRAM_NOTIFY_CHAT` | `channels.telegram.notify_chat` |
| `ANNA_TELEGRAM_GROUP_MODE` | `channels.telegram.group_mode` |
| `ANNA_TELEGRAM_ALLOWED_IDS` | `channels.telegram.allowed_ids` (comma-separated) |
| `ANNA_QQ_ENABLED` | `channels.qq.enabled` |
| `ANNA_QQ_ENABLE_NOTIFY` | `channels.qq.enable_notify` |
| `ANNA_QQ_APP_ID` | `channels.qq.app_id` |
| `ANNA_QQ_APP_SECRET` | `channels.qq.app_secret` |
| `ANNA_FEISHU_ENABLED` | `channels.feishu.enabled` |
| `ANNA_FEISHU_ENABLE_NOTIFY` | `channels.feishu.enable_notify` |
| `ANNA_FEISHU_APP_ID` | `channels.feishu.app_id` |
| `ANNA_FEISHU_APP_SECRET` | `channels.feishu.app_secret` |
| `ANNA_FEISHU_NOTIFY_CHAT` | `channels.feishu.notify_chat` |
| `ANNA_FEISHU_GROUP_MODE` | `channels.feishu.group_mode` |
| `ANNA_FEISHU_ALLOWED_IDS` | `channels.feishu.allowed_ids` (comma-separated) |
| `ANTHROPIC_API_KEY` | `providers.anthropic.api_key` |
| `ANTHROPIC_BASE_URL` | `providers.anthropic.base_url` |
| `OPENAI_API_KEY` | `providers.openai.api_key` (also used by `openai-response`) |
| `OPENAI_BASE_URL` | `providers.openai.base_url` (also used by `openai-response`) |

## Defaults

| Field | Default |
|-------|---------|
| `provider` | `anthropic` |
| `model` | `claude-sonnet-4-6` |
| `workspace` | `$ANNA_HOME/workspace` |
| `runner.type` | `go` |
| `runner.idle_timeout` | `10` (minutes) |
| `runner.compaction.max_tokens` | `80000` |
| `runner.compaction.keep_tail` | `20` |
| `scheduler.enabled` | `true` |
| `heartbeat.enabled` | `false` |
| `heartbeat.every` | `10m` |
| `channels.telegram.enabled` | `true` |
| `channels.telegram.enable_notify` | `false` |
| `channels.telegram.group_mode` | `mention` |
| `channels.qq.enabled` | `true` |
| `channels.qq.enable_notify` | `false` |
| `channels.qq.group_mode` | `mention` |
| `channels.feishu.enabled` | `true` |
| `channels.feishu.enable_notify` | `false` |
| `channels.feishu.group_mode` | `mention` |
