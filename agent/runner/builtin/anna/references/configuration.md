# Configuration Reference

Config file: `~/.anna/config.yaml`

## Minimal Setup

```yaml
providers:
  anthropic:
    api_key: "sk-..."

provider: anthropic
model: claude-sonnet-4-6
```

Or just: `export ANTHROPIC_API_KEY="sk-..."` and run `anna chat`.

## Full Config

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
    token: "BOT_TOKEN"
    notify_chat: "123456789"
    channel_id: "@my_channel"
    group_mode: "mention"          # mention | always | disabled
    allowed_ids: [136345060]
  qq:
    app_id: "QQ_BOT_APP_ID"
    app_secret: "QQ_BOT_APP_SECRET"
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

cron:
  enabled: true
  data_dir: "~/.anna/workspace/cron"
```

## Directory Layout

| Path | Purpose |
|------|---------|
| `~/.anna/config.yaml` | Static config (user-edited) |
| `~/.anna/workspace/state.yaml` | Runtime state: current provider/model (program-managed) |
| `~/.anna/cache/models.json` | Cached model list (safe to delete) |
| `~/.anna/workspace/sessions/` | Chat session history |
| `~/.anna/workspace/memory/` | Persistent memory (SOUL.md, USER.md, FACT.md, JOURNAL.jsonl) |
| `~/.anna/workspace/skills/` | Installed skills |
| `~/.anna/workspace/cron/` | Cron job persistence |

## Environment Variables

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
| `ANNA_CRON_ENABLED` | `cron.enabled` |
| `ANNA_TELEGRAM_TOKEN` | `channels.telegram.token` |
| `ANNA_TELEGRAM_NOTIFY_CHAT` | `channels.telegram.notify_chat` |
| `ANNA_TELEGRAM_GROUP_MODE` | `channels.telegram.group_mode` |
| `ANNA_TELEGRAM_ALLOWED_IDS` | `channels.telegram.allowed_ids` (comma-separated) |
| `ANNA_QQ_APP_ID` | `channels.qq.app_id` |
| `ANNA_QQ_APP_SECRET` | `channels.qq.app_secret` |
| `ANTHROPIC_API_KEY` | `providers.anthropic.api_key` |
| `ANTHROPIC_BASE_URL` | `providers.anthropic.base_url` |
| `OPENAI_API_KEY` | `providers.openai.api_key` (also used by `openai-response`) |
| `OPENAI_BASE_URL` | `providers.openai.base_url` (also used by `openai-response`) |

## Defaults

| Field | Default |
|-------|---------|
| `provider` | `anthropic` |
| `model` | `claude-sonnet-4-6` |
| `workspace` | `~/.anna/workspace` |
| `runner.type` | `go` |
| `runner.idle_timeout` | `10` (minutes) |
| `runner.compaction.max_tokens` | `80000` |
| `runner.compaction.keep_tail` | `20` |
| `cron.enabled` | `true` |
| `channels.telegram.group_mode` | `mention` |
| `channels.qq.group_mode` | `mention` |
