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

An administrator configures one vision setting for the whole deployment under **Settings -> Vision**. It creates a text description and transcription for images; it never answers you directly.

### Images in one-to-one conversations

When you send an image in an ordinary one-to-one conversation, Stella creates one immutable text baseline as the image arrives. That same baseline is used for the rest of the conversation, even if you switch models.

During the active turn that introduced the image — including any tool loop in that turn:

- A model whose **Input** declares `image` receives image pixels.
- A text-only or undeclared model receives the baseline immediately.

On later turns, every model receives the same baseline text, not the original pixels. If Stella cannot create a baseline, it uses the stable marker `[Image baseline unavailable.]` instead of retrying or inventing a description.

The original image remains visible in your authorized Web conversation history. Agents do not have a separate image-inspection tool.

To let a multimodal model receive image pixels during its active turn, open **Settings -> Providers**, edit the model, and set **Input** to `text, image`. This declaration controls active-turn native pixels only; it does not change how earlier images are represented.

## Switching models

You can switch models mid-conversation without losing context:

- **In any channel** -- type `/model` to see available models and pick a different one. On Telegram, this shows an inline keyboard for quick selection.
- **In the Web UI** -- go to the **Models** page to browse available models and switch the active model.

The change takes effect on the next message.
