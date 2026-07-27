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

The vision model reads images on behalf of a model that does not receive them itself. It transcribes the text in an image and describes what it shows, then hands the answering model that text. The vision model never answers you directly.

Unlike the agent tiers above, this is one setting for the whole deployment: an administrator picks it once under **Settings -> Vision**. Reading an image is infrastructure, not personality -- there is no reason for two agents to transcribe the same screenshot differently, and a per-agent setting would mean every new agent silently started blind.

### Which model sees the image

When an image turns up -- a photo you sent in a channel, or an image file the agent opens -- Stella takes the first of these that applies:

1. **The answering model itself**, if its **Input** modalities declare `image`. Full fidelity, no extra call.
2. **The vision model**, otherwise.
3. **Local text extraction (Xberg)**, if no vision model is set or the vision model fails. This reads text in an image but cannot describe a photo, chart, or layout.

The important part is step 1: **a model only receives images if you declare that it can.** Providers do not report their models' modalities, so a model you have not configured arrives undeclared, and Stella treats undeclared as "does not receive images". Handing an image to a model that cannot read it produces a blank placeholder that the agent then wastes a turn trying to work around, so the safe assumption is the useful one.

To give a multimodal model the image itself, open **Settings -> Providers**, edit the model, and set **Input** to `text, image`.

The ladder never falls back to an agent's default model: that is the model that did not receive the image to begin with.

## Switching models

You can switch models mid-conversation without losing context:

- **In any channel** -- type `/model` to see available models and pick a different one. On Telegram, this shows an inline keyboard for quick selection.
- **In the Web UI** -- go to the **Models** page to browse available models and switch the active model.

The change takes effect on the next message.
