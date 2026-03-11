# Configuration

Config file: `~/.anna/config.yaml`

The workspace root defaults to `~/.anna/workspace` and can be changed by setting the `ANNA_HOME` environment variable. Session, memory, skills, cron data, and runtime state all live under the workspace root. The model cache lives in `~/.anna/cache/`.

## Full Reference

```yaml
# Provider credentials and metadata
providers:
  anthropic:
    api_key: "sk-..."
    base_url: ""                   # Optional URL override
    models:                        # Optional model metadata
      - id: claude-sonnet-4-6
        name: "Claude Sonnet"
        reasoning: false           # Supports extended thinking
        input: ["text", "image"]   # Input modalities
        context_window: 200000
        max_tokens: 8192
        headers: {}                # Custom HTTP headers
        cost:
          input: 3.0               # Per-million-token pricing
          output: 15.0
          cache_read: 0.3
          cache_write: 3.75
  openai:
    api_key: "sk-..."
    base_url: "https://api.openai.com/v1"
  openai-response:                 # OpenAI-compatible APIs (e.g. Perplexity)
    api_key: "sk-..."
    base_url: "https://api.example.com/v1"

# Channel configuration
channels:
  telegram:
    enabled: true                  # Enable/disable this channel (default: true)
    enable_notify: false           # Allow notify tool to send to this channel (default: false)
    token: "BOT_TOKEN"
    notify_chat: "123456789"       # Chat ID for proactive notifications
    channel_id: "@my_channel"      # Optional broadcast channel
    group_mode: "mention"          # mention | always | disabled
    allowed_ids:                   # Restrict to these user IDs (empty = allow all)
      - 136345060
  qq:
    enabled: true                  # Enable/disable this channel (default: true)
    enable_notify: false           # Allow notify tool to send to this channel (default: false)
    app_id: "QQ_BOT_APP_ID"
    app_secret: "QQ_BOT_APP_SECRET"
    group_mode: "mention"          # mention | always | disabled
    allowed_ids: []                # User OpenIDs allowed (empty = allow all)
  feishu:
    enabled: true                  # Enable/disable this channel (default: true)
    enable_notify: false           # Allow notify tool to send to this channel (default: false)
    app_id: "FEISHU_APP_ID"
    app_secret: "FEISHU_APP_SECRET"
    encrypt_key: ""                # Event encrypt key (from developer console)
    verification_token: ""         # Event verification token (from developer console)
    notify_chat: "oc_xxx"          # Chat ID for proactive notifications
    group_mode: "mention"          # mention | always | disabled
    allowed_ids: []                # User open_ids allowed (empty = allow all)

# Default LLM provider
provider: anthropic
# Default model ID
model: claude-sonnet-4-6
# Tiered models (optional)
# Each tier falls back independently to model when not set
model_strong: claude-opus-4-6
model_fast: claude-haiku-4-5
# Workspace root (default: ANNA_HOME or ~/.anna/workspace)
workspace: "~/.anna/workspace"

# Runner settings
runner:
  type: go                       # Runner implementation (only "go" currently)
  system: ""                     # Custom system prompt (bypasses default builder)
  idle_timeout: 10               # Minutes before reaping idle runners
  compaction:
    max_tokens: 80000            # Auto-compact when history exceeds this
    keep_tail: 20                # Keep N recent messages after compaction

# Scheduled tasks
cron:
  enabled: true
  data_dir: "~/.anna/workspace/cron"  # Job persistence directory

# Heartbeat polling
heartbeat:
  enabled: false
  every: 10m
  file: "HEARTBEAT.md"                # Relative to workspace unless absolute
```

Heartbeat only runs in `anna gateway`. Each tick first uses the fast model to decide `skip` vs `run`, and only `run` decisions are sent into the main heartbeat session and then delivered through the notifier.

## Directory Layout

| Path | Purpose | Category |
|------|---------|----------|
| `~/.anna/config.yaml` | Static configuration (user-edited) | Config |
| `~/.anna/workspace/state.yaml` | Runtime state: current provider/model (program-managed) | State |
| `~/.anna/cache/models.json` | Cached model list (safe to delete) | Cache |
| `~/.anna/workspace/sessions/` | Chat session history | Data |
| `~/.anna/workspace/memory/` | Persistent memory (facts + journal) | Data |
| `~/.anna/workspace/skills/` | Installed skills | Data |
| `~/.anna/workspace/cron/` | Cron job persistence | Data |
| `~/.anna/workspace/lcm.db` | LCM SQLite database (messages + summaries) | Data |
| `~/.anna/workspace/HEARTBEAT.md` | Heartbeat instructions | Data |

- **config.yaml** is static and user-edited — safe to version control.
- **state.yaml** is written by `anna models set` and the `/model` command. It overrides `provider` and `model` from config.yaml.
- **cache/** contains regenerable data. Run `anna models update` to rebuild.
- **workspace/** contains all persistent application data.

## Environment Variable Overrides

All config fields support env var overrides using the `ANNA_` prefix. Nested structs add their own prefix segment (e.g. `runner.type` → `ANNA_RUNNER_TYPE`).

**Priority order** (highest wins): env vars → state.yaml → config.yaml → defaults.

| Variable | Overrides | Notes |
|----------|-----------|-------|
| `ANNA_HOME` | anna home directory | Default `~/.anna` |
| `ANNA_PROVIDER` | `provider` | |
| `ANNA_MODEL` | `model` | |
| `ANNA_MODEL_STRONG` | `model_strong` | |
| `ANNA_MODEL_FAST` | `model_fast` | |
| `ANNA_WORKSPACE` | `workspace` | |
| `ANNA_RUNNER_TYPE` | `runner.type` | |
| `ANNA_RUNNER_IDLE_TIMEOUT` | `runner.idle_timeout` | |
| `ANNA_CRON_ENABLED` | `cron.enabled` | |
| `ANNA_CRON_DATA_DIR` | `cron.data_dir` | |
| `ANNA_HEARTBEAT_ENABLED` | `heartbeat.enabled` | |
| `ANNA_HEARTBEAT_EVERY` | `heartbeat.every` | Go duration such as `10m` |
| `ANNA_HEARTBEAT_FILE` | `heartbeat.file` | Relative to workspace unless absolute |
| `ANNA_TELEGRAM_ENABLED` | `channels.telegram.enabled` | |
| `ANNA_TELEGRAM_ENABLE_NOTIFY` | `channels.telegram.enable_notify` | |
| `ANNA_TELEGRAM_TOKEN` | `channels.telegram.token` | |
| `ANNA_TELEGRAM_NOTIFY_CHAT` | `channels.telegram.notify_chat` | |
| `ANNA_TELEGRAM_CHANNEL_ID` | `channels.telegram.channel_id` | |
| `ANNA_TELEGRAM_GROUP_MODE` | `channels.telegram.group_mode` | |
| `ANNA_TELEGRAM_ALLOWED_IDS` | `channels.telegram.allowed_ids` | Comma-separated |
| `ANNA_QQ_ENABLED` | `channels.qq.enabled` | |
| `ANNA_QQ_ENABLE_NOTIFY` | `channels.qq.enable_notify` | |
| `ANNA_QQ_APP_ID` | `channels.qq.app_id` | |
| `ANNA_QQ_APP_SECRET` | `channels.qq.app_secret` | |
| `ANNA_QQ_GROUP_MODE` | `channels.qq.group_mode` | |
| `ANNA_QQ_ALLOWED_IDS` | `channels.qq.allowed_ids` | Comma-separated |
| `ANNA_FEISHU_ENABLED` | `channels.feishu.enabled` | |
| `ANNA_FEISHU_ENABLE_NOTIFY` | `channels.feishu.enable_notify` | |
| `ANNA_FEISHU_APP_ID` | `channels.feishu.app_id` | |
| `ANNA_FEISHU_APP_SECRET` | `channels.feishu.app_secret` | |
| `ANNA_FEISHU_ENCRYPT_KEY` | `channels.feishu.encrypt_key` | |
| `ANNA_FEISHU_VERIFICATION_TOKEN` | `channels.feishu.verification_token` | |
| `ANNA_FEISHU_NOTIFY_CHAT` | `channels.feishu.notify_chat` | |
| `ANNA_FEISHU_GROUP_MODE` | `channels.feishu.group_mode` | |
| `ANNA_FEISHU_ALLOWED_IDS` | `channels.feishu.allowed_ids` | Comma-separated |
| `ANTHROPIC_API_KEY` | `providers.anthropic.api_key` | Standard provider env |
| `ANTHROPIC_BASE_URL` | `providers.anthropic.base_url` | Standard provider env |
| `OPENAI_API_KEY` | `providers.openai.api_key` | Also used by `openai-response` |
| `OPENAI_BASE_URL` | `providers.openai.base_url` | Also used by `openai-response` |

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
| `cron.data_dir` | `~/.anna/workspace/cron` |
| `heartbeat.enabled` | `false` |
| `heartbeat.every` | `10m` |
| `heartbeat.file` | `~/.anna/workspace/HEARTBEAT.md` |
| `channels.telegram.enabled` | `true` |
| `channels.telegram.enable_notify` | `false` |
| `channels.telegram.group_mode` | `mention` |
| `channels.qq.enabled` | `true` |
| `channels.qq.enable_notify` | `false` |
| `channels.qq.group_mode` | `mention` |
| `channels.feishu.enabled` | `true` |
| `channels.feishu.enable_notify` | `false` |
| `channels.feishu.group_mode` | `mention` |

## LCM Defaults

Lossless Context Management (LCM) stores every message in a local SQLite database and builds a hierarchical summary tree so the model can recall earlier conversation without exceeding its context window. See [docs/session-compaction.md](session-compaction.md) for the compaction pipeline details.

The values below are compiled-in defaults (defined in `lcm/types.go`). They are **not yet exposed as config YAML options** but will become configurable in a future release.

| Parameter | Default | Description |
|-----------|---------|-------------|
| Database path | `~/.anna/workspace/lcm.db` | SQLite database derived from the workspace root |
| Fresh tail count | `20` | Number of recent messages always sent verbatim to the model |
| Context threshold | `0.75` | Fraction of the token budget that triggers automatic compaction |
| Leaf chunk size | `10` | Messages grouped per leaf summary during compaction |

### Compaction modes

| Mode | Trigger | Behavior |
|------|---------|----------|
| Incremental | Automatic after each turn (when threshold is exceeded) | Runs a single leaf pass plus optional condensation |
| Full | Manual via `/compact` command | Runs repeated passes until no further compaction is possible |
