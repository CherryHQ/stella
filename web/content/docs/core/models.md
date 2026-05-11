---
title: Model Management
---

## Tiered Models

Each agent in stella has three model fields, stored in the database (`settings_agents` table). The format for all model fields is `provider/model` (e.g. `anthropic/claude-sonnet-4-6`).

`provider` is usually the provider instance ID. As a compatibility fallback, stella also accepts a provider type alias such as `anthropic` or `openai` when there is exactly one configured provider of that type.

| Field          | Use Case                        |
| -------------- | ------------------------------- |
| `model`        | Default model for the agent     |
| `model_strong` | Heavy reasoning, complex tasks  |
| `model_fast`   | Quick responses, simple queries |

Both `model_strong` and `model_fast` fall back to `model` when not set. Configure these per-agent through the admin panel (web UI).

## Provider Setup

Providers are configured through the admin panel (web UI). Each provider is stored as a plugin in the `settings_plugins` table (kind=`provider`) with credentials in the `config` JSON field.

Environment variables serve as fallbacks when a provider's `api_key` field is empty in the database:

| Provider          | Environment Variable | Optional Variable |
| ----------------- | -------------------- | ----------------- |
| Anthropic         | `ANTHROPIC_API_KEY`  |                   |
| OpenAI            | `OPENAI_API_KEY`     | `OPENAI_BASE_URL` |
| OpenAI-Compatible | `OPENAI_API_KEY`     | `OPENAI_BASE_URL` |

The OpenAI-Compatible provider (`openai-response`) supports any service that implements the OpenAI Responses API, such as Perplexity or Together.ai.

## CLI Commands

```bash
stella models             # List available models (alias for list)
stella models list        # List all models grouped by provider
stella models update      # Fetch models from provider APIs and update cache
stella models current     # Show active provider/model
stella models set <p/m>   # Switch default agent's model (e.g. stella models set openai/gpt-4o)
stella models search <q>  # Search models by name
```

### Model Cache

`stella models update` queries all configured provider APIs and saves results to the `settings` table under the `models_cache` key. The cache is used by `list`, `search`, and the Telegram model picker.

You can also refresh the cache from the admin panel.

## Runtime Switching

Models can be switched at runtime without restarting:

- **CLI**: `/model` command during a chat session
- **Telegram**: Inline keyboard model picker
- **CLI command**: `stella models set provider/model` updates the default agent's model in the database

## Model Metadata

When the model cache is populated (via `stella models update` or the admin panel), each model entry includes metadata fetched from the provider API:

- Model ID
- Reasoning capability
- Supported input types (text, image)
- Context window size
- Max output tokens
- Cost per token (input, output, cache read, cache write)
- Custom headers

This metadata is used for model resolution, display, and cost tracking.
