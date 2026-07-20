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

Models are managed through the Web UI (web UI). You can browse available models, switch the active model, and refresh the model cache from the Models page.

## Provider setup

Create provider rows in the Web UI or API. Select **Anthropic**, **OpenAI**, or **OpenAI-compatible**, then store that provider's API key and base URL (when required) in its row. Provider credentials and base URLs are not read from server environment variables.

## Runtime switching

- **CLI**: `/model` in-chat command
- **Telegram**: inline keyboard model picker
- **Web UI**: switch the active model from the Models page
