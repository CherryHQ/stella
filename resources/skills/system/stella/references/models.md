# Model management

## Tiered models

Two tiers for different workloads, each falls back to `model` when not set:

| Tier   | Config Field   | Use Case                        |
| ------ | -------------- | ------------------------------- |
| strong | `model_strong` | Heavy reasoning, complex tasks  |
| fast   | `model_fast`   | Quick responses, simple queries |

```yaml
model: claude-sonnet-4-6 # default
model_strong: claude-opus-4-6 # optional
model_fast: claude-haiku-4-5 # optional
```

## CLI commands

```bash
stella models             # List available models
stella models list        # List all models grouped by provider
stella models update      # Fetch from provider APIs, update cache
stella models current     # Show active provider/model
stella models set <p/m>   # Switch (e.g. stella models set openai/gpt-4o)
stella models search <q>  # Search by name
```

The cache at `$STELLA_HOME/cache/models.json` is populated by `stella models update`. Without it, only models in config are shown.

## Provider setup

### Anthropic

```yaml
providers:
  anthropic:
    api_key: "sk-..."
```

Or: `export ANTHROPIC_API_KEY="sk-..."`

### OpenAI

```yaml
providers:
  openai:
    api_key: "sk-..."
    base_url: "https://api.openai.com/v1" # optional
```

Or: `export OPENAI_API_KEY="sk-..."`

### OpenAI-Compatible (Responses API)

For Perplexity, Together.ai, or any OpenAI-compatible service:

```yaml
providers:
  openai-response:
    api_key: "sk-..."
    base_url: "https://api.perplexity.ai"
```

Uses same `OPENAI_API_KEY` / `OPENAI_BASE_URL` env vars.

## Runtime switching

- **CLI**: `/model` in-chat command
- **Telegram**: inline keyboard model picker
- **Persistent**: `stella models set provider/model` writes to state.yaml
