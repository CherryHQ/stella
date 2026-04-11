# Configuration reference

All configuration is stored in a SQLite database at `$ANNA_HOME/anna.db` (`~/.anna/anna.db` by default).

The easiest way to configure anna is `anna --open`, which opens a web admin panel. The admin panel is also available during gateway operation via `anna --admin-port 8080`.

## Quick start

1. Run `anna --open` to open the admin panel
2. Add a provider (e.g., "anthropic" with your API key)
3. Create or edit an agent (set provider, model, system prompt, and sandbox network mode)
4. Configure channels (Telegram token, etc.)
5. Start: `anna chat` or `anna` (gateway daemon)

Or just: `export ANTHROPIC_API_KEY="sk-..."` and run `anna chat`. Default bootstrapping will create an "anthropic" provider and "anna" agent automatically.

## Database tables

All config lives in normalized SQLite tables:

| Table | Purpose |
|-------|---------|
| `settings` | Key-value JSON settings (runner, scheduler, heartbeat, plugins) |
| `settings_agents` | Agent definitions (provider/model, system prompt, workspace, per-agent sandbox config) |
| `settings_plugins` | Unified plugin table (tools, channels, hooks, providers). Provider credentials stored in `config` JSON. |
| `settings_users` | Auto-created platform users with default agent preference |
| `settings_channel_agents` | Per-group agent assignment |
| `ctx_agent_memory` | Per-user-per-agent persistent notes |

## Multi-agent setup

Each agent has:
- A provider + model configuration
- A system prompt (personality/identity)
- An isolated workspace at `$ANNA_HOME/workspaces/{agent_id}/`
- Its own skills directory

Create agents via the admin panel or directly in the database.

## Channel configuration

Channels are stored as plugin entries in the `settings_plugins` table (kind=channel). Configure via the admin panel.

**Telegram config fields:**
- `token` -- Bot token
- `channel_id` -- Broadcast channel ID or @username
- `group_mode` -- "mention" | "always" | "disabled"
- `enable_notify` -- Allow notify tool for this channel

Access control is handled by RBAC (auth_identities + policy engine). Notification targets are resolved from auth_identities.

**QQ config fields:** `app_id`, `app_secret`, `group_mode`, `enable_notify`

**Feishu config fields:** `app_id`, `app_secret`, `encrypt_key`, `verification_token`, `group_mode`, `enable_notify`

Feishu is a chat channel only. Lark workspace operations no longer ship as built-in `feishu_*` tools; add a `lark-cli` skill yourself if you want that workflow.

## Settings (key-value)

Global settings are stored in the `settings` table as JSON values:

| Key | Purpose |
|-----|---------|
| `runner` | Runner type, idle timeout, compaction config |
| `scheduler` | Scheduler enabled flag, data directory |
| `heartbeat` | Heartbeat enabled, interval, file path |
| `plugins` | Array of plugin configs (path + optional config) |

## Directory layout

All paths are relative to `$ANNA_HOME` (`~/.anna` by default).

| Path | Purpose |
|------|---------|
| `anna.db` | SQLite database (all config + runtime data) |
| `cache/models.json` | Cached model list (safe to delete) |
| `cache/sandbox/` | Sandbox scratch/preflight state |
| `workspaces/{agent_id}/` | Per-agent workspace |
| `workspaces/{agent_id}/skills/` | Per-agent installed skills |
| `workspaces/{agent_id}/anna.log` | Per-agent log |

## Sandbox settings

Sandbox settings are stored per agent in the `settings_agents.sandbox` JSON column and can be edited in the admin panel on each agent:

- `network.mode` — `disabled` (default), `allow_all`, or `whitelist`
- `network.allowlist` — required only when mode is `whitelist`; entries may be hostnames, IPs, or CIDRs

Linux and macOS validate the managed `boxsh` binary, workspace/state-dir shape, and per-agent network policy, then run the core local-workspace tools (`bash`, `read`, `write`, `edit`) through a shared `boxsh --rpc` sandbox backend. Windows keeps the existing non-`boxsh` behavior.

## Environment variables

Environment variables serve as fallbacks for provider API keys:

| Variable | Fallback for |
|----------|-------------|
| `ANNA_HOME` | anna home directory (default `~/.anna`) |
| `ANTHROPIC_API_KEY` | anthropic provider API key |
| `ANTHROPIC_BASE_URL` | anthropic provider base URL |
| `OPENAI_API_KEY` | openai/openai-response provider API key |
| `OPENAI_BASE_URL` | openai/openai-response provider base URL |

Note: The old YAML-based environment variables (`ANNA_PROVIDER`, `ANNA_MODEL`, `ANNA_TELEGRAM_TOKEN`, etc.) are no longer supported. Use the admin panel or database directly.

## Defaults

On first run, `SeedDefaults` creates:
- All built-in provider plugins (anthropic, openai, openai-response) with env var fallback for API keys
- An "anna" agent using the anthropic provider with `claude-sonnet-4-6` model
- Default system prompt with anna's personality
- All built-in tool, channel, and hook plugins
