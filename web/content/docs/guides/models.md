---
title: Models
---

Stella works with multiple AI providers and lets you choose which model powers your conversations. You can switch models on the fly without restarting.

## Supported providers

Stella supports three types of providers:

- **Anthropic** -- Claude models (Sonnet, Opus, Haiku)
- **OpenAI** -- GPT and o-series models
- **OpenAI-compatible** -- any service that implements the OpenAI API, such as Perplexity, Together.ai, or a local model server

You can configure multiple providers at the same time and mix models from different providers across your agents.

## Setting up a provider

1. Open the Web UI.
2. Go to the **Providers** section.
3. Click **Add provider** and choose the provider type.
4. Enter your API key and any required settings (such as a custom base URL for OpenAI-compatible providers).
5. Save the provider.

Once saved, Stella fetches the available models from that provider automatically.

## Model tiers

Each agent in Stella uses up to three model tiers:

| Tier        | When Stella uses it                                   |
| ----------- | ----------------------------------------------------- |
| **Default** | Everyday conversations and general tasks              |
| **Strong**  | Hard problems, complex reasoning, multi-step analysis |
| **Fast**    | Quick checks, simple lookups, lightweight subtasks    |

If you only set the default model, Stella uses it for everything. The strong and fast tiers are optional -- set them when you want Stella to pick the right tool for the job automatically.

Configure model tiers per agent in the Web UI on the agent settings page.

## Vision model

The vision model reads images on behalf of a model that cannot see them. When a model that does not accept image input meets one -- a photo you sent in a channel, or an image file the agent opens -- Stella asks the vision model to transcribe the text in it and describe what it shows, then hands the answering model that text. The vision model never answers you directly.

Unlike the agent tiers above, this is one setting for the whole deployment: an administrator picks it once under **Settings -> Vision**. Reading an image is infrastructure, not personality -- there is no reason for two agents to transcribe the same screenshot differently, and a per-agent setting would mean every new agent silently started blind.

Leave it unset and Stella falls back to local text extraction, which reads text in an image but cannot describe a photo, chart, or layout. It never falls back to an agent's default model: that is the model that could not read the image to begin with.

## Switching models

You can switch models mid-conversation without losing context:

- **In any channel** -- type `/model` to see available models and pick a different one. On Telegram, this shows an inline keyboard for quick selection.
- **In the Web UI** -- go to the **Models** page to browse available models and switch the active model.

The change takes effect on the next message.
