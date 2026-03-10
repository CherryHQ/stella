# Model Management

## Tiered Models

Two tiers for different workloads, each falls back to `model` when not set:

| Tier | Config Field | Use Case |
|------|-------------|----------|
| strong | `model_strong` | Heavy reasoning, complex tasks |
| fast | `model_fast` | Quick responses, simple queries |

```yaml
model: claude-sonnet-4-6          # default
model_strong: claude-opus-4-6     # optional
model_fast: claude-haiku-4-5      # optional
```

## CLI Commands

```bash
anna models             # List available models
anna models list        # List all models grouped by provider
anna models update      # Fetch from provider APIs, update cache
anna models current     # Show active provider/model
anna models set <p/m>   # Switch (e.g. anna models set openai/gpt-4o)
anna models search <q>  # Search by name
```

The cache at `~/.anna/cache/models.json` is populated by `anna models update`. Without it, only models in config are shown.

## Provider Setup

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
    base_url: "https://api.openai.com/v1"  # optional
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

## Runtime Switching

- **CLI**: `/model` in-chat command
- **Telegram**: inline keyboard model picker
- **Persistent**: `anna models set provider/model` writes to state.yaml
