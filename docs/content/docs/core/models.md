---
title: Model Management
---

## Tiered Models

Each agent in anna has three model fields, stored in the database (`settings_agents` table). The format for all model fields is `provider/model` (e.g. `anthropic/claude-sonnet-4-6`).

| Field          | Use Case                        |
| -------------- | ------------------------------- |
| `model`        | Default model for the agent     |
| `model_strong` | Heavy reasoning, complex tasks  |
| `model_fast`   | Quick responses, simple queries |

Both `model_strong` and `model_fast` fall back to `model` when not set. Configure these per-agent through the admin panel (`anna --open`).

## Provider Setup

Providers are configured through the admin panel (`anna --open`). Each provider is stored in the `settings_providers` table with an optional API key and base URL.

Environment variables serve as fallbacks when a provider's `api_key` field is empty in the database:

| Provider          | Environment Variable | Optional Variable |
| ----------------- | -------------------- | ----------------- |
| Anthropic         | `ANTHROPIC_API_KEY`  |                   |
| OpenAI            | `OPENAI_API_KEY`     | `OPENAI_BASE_URL` |
| OpenAI-Compatible | `OPENAI_API_KEY`     | `OPENAI_BASE_URL` |

The OpenAI-Compatible provider (`openai-response`) supports any service that implements the OpenAI Responses API, such as Perplexity or Together.ai.

## CLI Commands

```bash
anna models             # List available models (alias for list)
anna models list        # List all models grouped by provider
anna models update      # Fetch models from provider APIs and update cache
anna models current     # Show active provider/model
anna models set <p/m>   # Switch default agent's model (e.g. anna models set openai/gpt-4o)
anna models search <q>  # Search models by name
```

### Model Cache

`anna models update` queries all configured provider APIs and saves results to the `settings` table under the `models_cache` key. The cache is used by `list`, `search`, and the Telegram model picker.

You can also refresh the cache from the admin panel.

## Runtime Switching

Models can be switched at runtime without restarting:

- **CLI**: `/model` command during a chat session
- **Telegram**: Inline keyboard model picker
- **CLI command**: `anna models set provider/model` updates the default agent's model in the database

## Model Metadata

When the model cache is populated (via `anna models update` or the admin panel), each model entry includes metadata fetched from the provider API:

- Model ID
- Reasoning capability
- Supported input types (text, image)
- Context window size
- Max output tokens
- Cost per token (input, output, cache read, cache write)
- Custom headers

This metadata is used for model resolution, display, and cost tracking.
