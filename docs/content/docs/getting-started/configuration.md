---
title: Configuration
---

All configuration is stored in a single SQLite database at `~/.anna/anna.db`. There are no YAML config files. To set up or modify your configuration, run `anna --open` to open the web admin panel.

The home directory defaults to `~/.anna` and can be changed by setting the `ANNA_HOME` environment variable.

## Database Tables

Configuration is spread across several tables in the SQLite database.

### settings

A key-value store for global settings. Each row has a `key` (text) and a `value` (JSON text). Known keys:

| Key            | Description                                                   |
| -------------- | ------------------------------------------------------------- |
| `runner`       | Runner type, system prompt, idle timeout, compaction settings |
| `compaction`   | Compaction thresholds (max_tokens, keep_tail)                 |
| `heartbeat`    | Heartbeat polling toggle and interval                         |
| `plugins`      | JSON array of plugin definitions                              |
| `runtime_plugins` | JSON object of subprocess tool/channel bindings            |
| `models_cache` | Cached model list from providers                              |

### settings_agents

One row per agent.

| Column          | Type    | Description                                                      |
| --------------- | ------- | ---------------------------------------------------------------- |
| `id`            | TEXT    | Agent slug (e.g. `anna`)                                         |
| `name`          | TEXT    | Display name                                                     |
| `model`         | TEXT    | Default model in `provider/model` format                         |
| `model_strong`  | TEXT    | Strong-tier model in `provider/model` format                     |
| `model_fast`    | TEXT    | Fast-tier model in `provider/model` format                       |
| `system_prompt` | TEXT    | Custom system prompt (bypasses default builder)                  |
| `workspace`     | TEXT    | Absolute path to agent workspace directory                       |
| `sandbox`       | TEXT    | JSON sandbox config for the agent (`backend`, `network.mode`, `network.allowlist`) |
| `enabled`       | INTEGER | 1 = active, 0 = disabled                                         |

### settings_channels

One row per channel instance. Multiple rows can share the same platform `type`, such as multiple Feishu bot instances.

| Column    | Type    | Description                                           |
| --------- | ------- | ----------------------------------------------------- |
| `id`      | TEXT    | Channel instance identifier, such as `telegram` or `feishu-coder` |
| `type`    | TEXT    | Platform type: `telegram`, `qq`, `feishu`, or `weixin` |
| `agent_id` | TEXT   | Optional dedicated agent for this channel instance |
| `enabled` | INTEGER | 1 = active, 0 = disabled                              |
| `config`  | TEXT    | JSON blob with platform-specific settings (see below) |

### settings_users

Maps external platform users to agents.

| Column             | Type    | Description                                               |
| ------------------ | ------- | --------------------------------------------------------- |
| `id`               | INTEGER | Auto-increment primary key                                |
| `external_id`      | TEXT    | User ID on the platform                                   |
| `platform`         | TEXT    | Platform identifier (e.g. `telegram`)                     |
| `name`             | TEXT    | Display name                                              |
| `default_agent_id` | TEXT    | FK to `settings_agents.id` -- default agent for this user |

### settings_channel_agents

Routes a specific group chat to a specific agent.

| Column     | Type | Description                      |
| ---------- | ---- | -------------------------------- |
| `channel_id` | TEXT | Channel instance identifier |
| `platform` | TEXT | Platform identifier              |
| `chat_id`  | TEXT | Group or chat ID on the platform |
| `agent_id` | TEXT | FK to `settings_agents.id`       |

Composite primary key: `(channel_id, chat_id)`.

## Runner Settings

Stored in the `settings` table under the `runner` key as a JSON object.

| Field                   | Default | Description                                     |
| ----------------------- | ------- | ----------------------------------------------- |
| `type`                  | `go`    | Runner implementation (only `go` currently)     |
| `system`                | `""`    | Custom system prompt (bypasses default builder) |
| `idle_timeout`          | `10`    | Minutes before reaping idle runners             |
| `compaction.max_tokens` | `80000` | Auto-compact when history exceeds this          |
| `compaction.keep_tail`  | `20`    | Keep N recent messages after compaction         |

## Channel Config Blobs

Each platform stores its own JSON structure in the `config` column of `settings_channels`.

**Telegram**

```json
{
  "token": "BOT_TOKEN",
  "enable_notify": false,
  "notify_chat": "123456789",
  "channel_id": "@my_channel",
  "group_mode": "mention",
  "allowed_ids": [136345060]
}
```

**QQ**

```json
{
  "app_id": "QQ_BOT_APP_ID",
  "app_secret": "QQ_BOT_APP_SECRET",
  "enable_notify": false,
  "group_mode": "mention",
  "allowed_ids": []
}
```

**Feishu**

```json
{
  "app_id": "FEISHU_APP_ID",
  "app_secret": "FEISHU_APP_SECRET",
  "encrypt_key": "",
  "verification_token": "",
  "enable_notify": false,
  "notify_chat": "oc_xxx",
  "group_mode": "mention",
  "allowed_ids": []
}
```

`group_mode` accepts `mention`, `always`, or `disabled`.

## Directory Layout

| Path                                         | Purpose                                     | Category |
| -------------------------------------------- | ------------------------------------------- | -------- |
| `~/.anna/anna.db`                            | SQLite database (config, memory, scheduler) | Data     |
| `~/.anna/plugins/bundled/`                   | Bundled runtime plugin manifests            | Data     |
| `~/.anna/plugins/installed/`                 | User-installed runtime plugins              | Data     |
| `~/.anna/workspaces/{agent-id}/skills/`      | Per-agent installed skills                  | Data     |
| `~/.anna/workspaces/{agent-id}/anna.log`     | Per-agent log file                          | Data     |
| `~/.anna/workspaces/{agent-id}/SOUL.md`      | Optional soul/identity override             | Data     |
| `~/.anna/workspaces/{agent-id}/SYSTEM.md`    | Optional system prompt override             | Data     |
| `~/.anna/workspaces/{agent-id}/HEARTBEAT.md` | Heartbeat instructions                      | Data     |
| `~/.anna/cache/`                             | Model cache (safe to delete)                | Cache    |
| `~/.anna/cache/sandbox/`                     | Sandbox session scratch/preflight state     | Cache    |

- **anna.db** is the single source of truth for all configuration, memory, and scheduler data.
- **workspaces/** contains per-agent data. Each agent gets its own directory keyed by agent ID.
- **cache/** contains regenerable data. Run `anna models update` to rebuild.
- **agent sandbox config** lives on each agent record (`settings_agents.sandbox`). The agent form edits network policy; backend can also be set in the stored JSON.
  - `backend`: `auto` (default), `boxsh`, or `local`
  - `network.mode`: `disabled` (default), `allow_all`, or `whitelist`
  - `network.allowlist`: required only when mode is `whitelist`
  - Linux and macOS validate the managed `boxsh` backend when selected. Runner startup fails closed when the sandbox backend is unavailable or cannot enforce the configured network mode.
  - `local` is a relaxed backend with advisory enforcement; use it only for explicit local/development scenarios.
  - Current `boxsh` client builds may reject `whitelist` mode at runtime; use it only when your runtime supports whitelist enforcement.

## Environment Variables

The old `ANNA_*` prefix overrides for all config fields are removed. Only the following environment variables are recognized:

| Variable              | Purpose                                          |
| --------------------- | ------------------------------------------------ |
| `ANNA_HOME`           | Override the home directory (default `~/.anna`)  |
| `ANTHROPIC_API_KEY`   | Fallback API key for the Anthropic provider      |
| `ANTHROPIC_BASE_URL`  | Fallback base URL for the Anthropic provider     |
| `OPENAI_API_KEY`      | Fallback API key for the OpenAI provider         |
| `OPENAI_BASE_URL`     | Fallback base URL for the OpenAI provider        |

All other configuration must be set through the admin panel (`anna --open`) or directly in the database.

## Memory Defaults

Lossless Context Management settings are currently hardcoded defaults. They will become configurable in a future release.

| Setting           | Default | Description                                         |
| ----------------- | ------- | --------------------------------------------------- |
| Fresh tail count  | `20`    | Number of recent messages kept verbatim in context  |
| Context threshold | `0.75`  | Fraction of context window that triggers compaction |
| Leaf chunk size   | `10`    | Number of messages grouped per leaf summary         |

## Heartbeat

Heartbeat only runs in server mode (`anna`). Configuration is stored in the `settings` table under the `heartbeat` key. Each tick first uses the fast model to decide `skip` vs `run`, and only `run` decisions are sent into the main heartbeat session and then delivered through the notifier. Instructions are read from the agent's `HEARTBEAT.md` file.

## Plugins

Plugins are stored in the `settings` table under the `plugins` key as a JSON array. Each entry has a `path` to a JS file and an optional `config` object:

```json
[
  { "path": "~/plugins/hello.js" },
  { "path": "/abs/path/notify.js", "config": { "webhook_url": "https://example.com" } }
]
```

## Runtime Plugin Bindings

Subprocess plugin bindings are stored separately under the `runtime_plugins` key:

```json
{
  "tools": {
    "read": "tool/read",
    "bash": "tool/bash"
  },
  "channels": {
    "telegram": "channel/telegram",
    "qq": "channel/qq"
  }
}
```

These bindings control which runtime plugin ID handles each built-in tool or channel slot. If a slot has no explicit override, anna falls back to the bundled first-party plugin for that slot. Use `anna plugin runtime list` to inspect the effective bindings and `anna plugin runtime bind ...` to change them.
