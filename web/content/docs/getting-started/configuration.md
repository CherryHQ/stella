---
title: Configuration
---

All configuration is stored in a single SQLite database at `~/.stella/stella.db`. There are no YAML config files. To set up or modify your configuration, run `stella` and open the web UI at `http://localhost:25678`.

The home directory defaults to `~/.stella` and can be changed by setting the `STELLA_HOME` environment variable.

## Database Tables

Configuration is spread across several tables in the SQLite database.

### settings

A key-value store for global settings. Each row has a `key` (text) and a `value` (JSON text). Known keys:

| Key               | Description                                                   |
| ----------------- | ------------------------------------------------------------- |
| `runner`          | Runner type, system prompt, idle timeout, compaction settings |
| `compaction`      | Compaction thresholds (max_tokens, keep_tail)                 |
| `heartbeat`       | Heartbeat polling toggle and interval                         |
| `plugins`         | JSON array of plugin definitions                              |
| `runtime_plugins` | JSON object of subprocess tool/channel bindings               |
| `models_cache`    | Cached model list from providers                              |

### settings_agents

One row per agent.

| Column          | Type    | Description                                                                        |
| --------------- | ------- | ---------------------------------------------------------------------------------- |
| `id`            | TEXT    | Agent slug (e.g. `stella`)                                                         |
| `name`          | TEXT    | Display name                                                                       |
| `model`         | TEXT    | Default model in `provider/model` format                                           |
| `model_strong`  | TEXT    | Strong-tier model in `provider/model` format                                       |
| `model_fast`    | TEXT    | Fast-tier model in `provider/model` format                                         |
| `system_prompt` | TEXT    | Custom system prompt (bypasses default builder)                                    |
| `workspace`     | TEXT    | Absolute path to agent workspace directory                                         |
| `sandbox`       | TEXT    | JSON sandbox config for the agent (`backend`, `network.mode`, `network.allowlist`) |
| `enabled`       | INTEGER | 1 = active, 0 = disabled                                                           |

### settings_channels

One row per channel instance. Multiple rows can share the same platform `type`, such as multiple Feishu bot instances.

| Column     | Type    | Description                                                       |
| ---------- | ------- | ----------------------------------------------------------------- |
| `id`       | TEXT    | Channel instance identifier, such as `telegram` or `feishu-coder` |
| `type`     | TEXT    | Platform type: `telegram`, `qq`, `feishu`, or `weixin`            |
| `agent_id` | TEXT    | Optional dedicated agent for this channel instance                |
| `enabled`  | INTEGER | 1 = active, 0 = disabled                                          |
| `config`   | TEXT    | JSON blob with platform-specific settings (see below)             |

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

| Column       | Type | Description                      |
| ------------ | ---- | -------------------------------- |
| `channel_id` | TEXT | Channel instance identifier      |
| `platform`   | TEXT | Platform identifier              |
| `chat_id`    | TEXT | Group or chat ID on the platform |
| `agent_id`   | TEXT | FK to `settings_agents.id`       |

Composite primary key: `(channel_id, chat_id)`.

## Runner Settings

Stored in the `settings` table under the `runner` key as a JSON object.

| Field                   | Default | Description                                                                                                                                                        |
| ----------------------- | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `type`                  | `go`    | Runner implementation (only `go` currently)                                                                                                                        |
| `system`                | `""`    | Custom system prompt (bypasses default builder)                                                                                                                    |
| `idle_timeout`          | `10`    | Minutes before reaping idle runners                                                                                                                                |
| `subagent_timeout`      | `15`    | Default wall-clock timeout (minutes) for each subagent run spawned by the `agent` tool. Individual presets can override this with a `timeout:` front-matter field. |
| `compaction.max_tokens` | `80000` | Auto-compact when history exceeds this                                                                                                                             |
| `compaction.keep_tail`  | `20`    | Keep N recent messages after compaction                                                                                                                            |

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

| Path                                           | Purpose                                     | Category |
| ---------------------------------------------- | ------------------------------------------- | -------- |
| `~/.stella/stella.db`                          | SQLite database (config, memory, scheduler) | Data     |
| `~/.stella/plugins/bundled/`                   | Bundled runtime plugin manifests            | Data     |
| `~/.stella/plugins/installed/`                 | User-installed runtime plugins              | Data     |
| `~/.stella/workspaces/{agent-id}/skills/`      | Per-agent installed skills                  | Data     |
| `~/.stella/workspaces/{agent-id}/stella.log`   | Per-agent log file                          | Data     |
| `~/.stella/workspaces/{agent-id}/SOUL.md`      | Optional soul/identity override             | Data     |
| `~/.stella/workspaces/{agent-id}/SYSTEM.md`    | Optional system prompt override             | Data     |
| `~/.stella/workspaces/{agent-id}/HEARTBEAT.md` | Heartbeat instructions                      | Data     |
| `~/.stella/cache/`                             | Model cache (safe to delete)                | Cache    |
| `~/.stella/cache/sandbox/`                     | Sandbox session scratch/preflight state     | Cache    |

- **stella.db** is the single source of truth for all configuration, memory, and scheduler data.
- **workspaces/** contains per-agent data. Each agent gets its own directory keyed by agent ID.
- **cache/** contains regenerable data. Refresh from the admin panel to rebuild.
- **agent sandbox config** lives on each agent record (`settings_agents.sandbox`). The agent form edits network policy; backend can also be set in the stored JSON.
  - `backend`: `auto` (default), `local`, or `docker`
  - `network.mode`: `disabled` (default), `allow_all`, or `whitelist`
  - `network.allowlist`: required only when mode is `whitelist`
  - Linux and macOS validate the `local` backend when selected. Runner startup fails closed when the sandbox backend is unavailable or cannot enforce the configured network mode.
  - `auto` selects `local` on Linux/macOS; on other platforms it fails closed — configure `docker` explicitly.
  - `local` may reject `whitelist` mode at runtime; use it only when your runtime supports whitelist enforcement.
  - `docker` runs each session in a dedicated container built from the bundled `plugins/sandbox/docker/Dockerfile`. The image is version-locked to the stella binary: dev builds use a local `stella-sandbox:dev` tag (produced by `mise run sandbox:docker:build`), and tagged releases pull `ghcr.io/cherryhq/stella-sandbox:<version>` from GHCR. It is opt-in; `auto` does not select it. Requires a reachable docker daemon. Useful on Windows (where `local` is unavailable) and for workflows that need a specific Linux userspace.
  - The docker backend takes no per-agent knobs. The image, the in-container user (`stella`, UID 1000), and bind-mount layout are all fixed by the shipped image. When stella itself runs inside a container and talks to a host docker daemon (Docker-outside-of-Docker), set the `STELLA_HOME_HOST` environment variable — stella auto-derives the path translation from it.
  - Docker backend does **not** currently implement `whitelist` network mode or HTTP mediation — it fails closed, the same as `local`. Use `disabled` or `allow_all`.
  - Docker bind-mounts the workspace root at `/home/stella/workspace` and each read-only path at `/home/stella/readonly/<index>`. Scripts that rely on absolute host paths will need to use the in-container path instead.

Example JSON for a docker-backed agent's `sandbox` field:

```json
{
  "backend": "docker",
  "network": { "mode": "allow_all" }
}
```

## Environment Variables

The old `STELLA_*` prefix overrides for all config fields are removed. Only the following environment variables are recognized:

| Variable             | Purpose                                                                                                                                                                      |
| -------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `STELLA_HOME`        | Override the home directory (default `~/.stella`)                                                                                                                            |
| `STELLA_HOME_HOST`   | Host-side path of `STELLA_HOME` when stella runs inside a container and talks to a host docker daemon (Docker-outside-of-Docker). Required in that setup; ignored otherwise. |
| `ANTHROPIC_API_KEY`  | Fallback API key for the Anthropic provider                                                                                                                                  |
| `ANTHROPIC_BASE_URL` | Fallback base URL for the Anthropic provider                                                                                                                                 |
| `OPENAI_API_KEY`     | Fallback API key for the OpenAI provider                                                                                                                                     |
| `OPENAI_BASE_URL`    | Fallback base URL for the OpenAI provider                                                                                                                                    |

All other configuration must be set through the web UI or directly in the database.

## Memory Defaults

Lossless Context Management settings are currently hardcoded defaults. They will become configurable in a future release.

| Setting           | Default | Description                                         |
| ----------------- | ------- | --------------------------------------------------- |
| Fresh tail count  | `20`    | Number of recent messages kept verbatim in context  |
| Context threshold | `0.75`  | Fraction of context window that triggers compaction |
| Leaf chunk size   | `10`    | Number of messages grouped per leaf summary         |

## Heartbeat

Heartbeat only runs in server mode (`stella`). Configuration is stored in the `settings` table under the `heartbeat` key. Each tick first uses the fast model to decide `skip` vs `run`, and only `run` decisions are sent into the main heartbeat session and then delivered through the notifier. Instructions are read from the agent's `HEARTBEAT.md` file.

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

These bindings control which runtime plugin ID handles each built-in tool or channel slot. If a slot has no explicit override, stella falls back to the bundled first-party plugin for that slot. Manage runtime bindings through the admin panel.
