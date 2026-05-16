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

## Managing models

Models are managed through the admin panel (web UI). You can browse available models, switch the active model, and refresh the model cache from the Models page.

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
- **Admin panel**: switch the active model from the Models page
