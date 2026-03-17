# Configuration reference

All configuration is stored in a SQLite database at `$ANNA_HOME/anna.db` (`~/.anna/anna.db` by default).

The easiest way to configure anna is `anna onboard`, which opens a web admin panel. The admin panel is also available during gateway operation via `anna gateway --admin-port 8080`.

## Quick start

1. Run `anna onboard` to open the admin panel
2. Add a provider (e.g., "anthropic" with your API key)
3. Create or edit an agent (set provider, model, system prompt)
4. Configure channels (Telegram token, etc.)
5. Start: `anna chat` or `anna gateway`

Or just: `export ANTHROPIC_API_KEY="sk-..."` and run `anna chat`. Default bootstrapping will create an "anthropic" provider and "anna" agent automatically.

## Database tables

All config lives in normalized SQLite tables:

| Table | Purpose |
|-------|---------|
| `settings` | Key-value JSON settings (runner, scheduler, heartbeat, plugins) |
| `settings_providers` | LLM API providers (API key, base URL) |
| `settings_agents` | Agent definitions (provider, model, system prompt, workspace) |
| `settings_channels` | Platform configs (Telegram/QQ/Feishu as JSON blobs) |
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

Channels are stored as JSON blobs in the `settings_channels` table. Configure via the admin panel.

**Telegram config fields:**
- `token` -- Bot token
- `notify_chat` -- Default chat ID for notifications
- `channel_id` -- Broadcast channel ID or @username
- `group_mode` -- "mention" | "always" | "disabled"
- `allowed_ids` -- Array of user IDs (empty = allow all)
- `enable_notify` -- Allow notify tool for this channel

**QQ config fields:** `app_id`, `app_secret`, `group_mode`, `allowed_ids`, `enable_notify`

**Feishu config fields:** `app_id`, `app_secret`, `encrypt_key`, `verification_token`, `notify_chat`, `group_mode`, `allowed_ids`, `enable_notify`

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
| `workspaces/{agent_id}/` | Per-agent workspace |
| `workspaces/{agent_id}/skills/` | Per-agent installed skills |
| `workspaces/{agent_id}/anna.log` | Per-agent log |

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
- An "anthropic" provider (with env var fallback for API key)
- An "anna" agent using the anthropic provider with `claude-sonnet-4-6` model
- Default system prompt with anna's personality
