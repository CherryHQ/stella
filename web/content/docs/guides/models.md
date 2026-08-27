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

The original image remains visible in your authorized Web conversation history.

### Inspect an existing image with `view_image`

An agent can call `view_image(path, prompt?)` to inspect an image file in its sandbox. The tool accepts PNG, JPEG, GIF, and WebP files. Relative paths use the agent's current working directory; `$HOME`, `$STELLA_ASSETS_DIR`, and `$TMPDIR` refer to the sandbox roots shown to the agent. Stella reads the file through the sandbox and never exposes its physical host path.

Stella chooses the result from the effective model for that turn:

| Current model and vision setting                                                    | Result                                                                                                             |
| ----------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------ |
| The current model declares `image` input                                            | The model receives the verified image pixels. The optional `prompt` does not invoke the separate vision model.     |
| The current model does not declare `image`, and a usable vision model is configured | The vision model receives the image and returns textual evidence. `prompt` focuses what it should inspect.         |
| No usable vision model is configured, and `prompt` is omitted                       | Stella returns a generic transcription and scene baseline when one can be produced.                                |
| No usable vision model is configured, and `prompt` is present                       | The tool returns an actionable error because a generic baseline cannot honestly answer a targeted visual question. |

The current model means the model actually selected for this turn, including a model switch made before the model call. An undeclared image capability is treated like text-only input: Stella does not risk sending it pixels.

Selecting a model under **Settings -> Vision** declares that it can inspect images when the provider supplies no input-capability metadata. If that model explicitly declares text-only input, Stella treats it as unavailable and never sends it image bytes. A vision-provider failure or a failed generic baseline is returned as an error instead of being presented as a successful inspection.

Text produced from an image is wrapped as untrusted evidence. Text inside the image can be quoted or analyzed, but it is not treated as an instruction to the agent.

`view_image` validates the actual file contents before either route. Filename extensions do not determine the format; Stella detects it from the bytes. Damaged, unsupported, or unsafe image contents are rejected. Use `bash` with `xberg extract` for documents such as PDF, DOCX, XLSX, and PPTX. `view_image` does not generate or edit images.

To let a multimodal model receive image pixels during its active turn, open **Settings -> Providers**, edit the model, and set **Input** to `text, image`. This declaration controls active-turn native pixels and the pixel route of `view_image`; it does not change how earlier conversation images are represented.

## Switching models

You can switch models from the **Models** page in the Web UI. Open the page to browse available models and switch the active model.

The change takes effect on the next message.
