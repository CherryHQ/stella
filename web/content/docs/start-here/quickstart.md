---
title: Quick Start
---

Get Stella running and have your first AI conversation in under five minutes.

## Install

**Homebrew (macOS and Linux):**

```bash
brew install CherryHQ/tap/stella
```

**Go install:**

```bash
go install github.com/CherryHQ/stella/cmd/stellad@latest
```

**Binary download:**

Grab the latest binary from [Releases](https://github.com/CherryHQ/stella/releases), or self-update an existing install with:

```bash
stellad upgrade
```

## Set up your API key

You need an API key from at least one provider. Stella works with Anthropic, OpenAI, and any OpenAI-compatible API.

Set your key as an environment variable so Stella can find it on startup:

```bash
# Pick one (or both)
export ANTHROPIC_API_KEY="sk-ant-..."
export OPENAI_API_KEY="sk-..."
```

You can also add these to your shell profile (`~/.zshrc`, `~/.bashrc`, etc.) so they persist across sessions.

## Start the server

```bash
stellad server
```

Stella starts and serves the Web UI at [http://localhost:25678](http://localhost:25678). Open it in your browser.

To use a different port:

```bash
stellad server --port 8080
```

## Configure a provider

1. Open the Web UI at [http://localhost:25678](http://localhost:25678).
2. Go to **Providers** in the sidebar.
3. Click **Add Provider** and enter your API key.
4. Stella will auto-detect available models from your provider.

If you already set an environment variable in the previous step, Stella picks it up automatically as a fallback. Configuring the provider in the Web UI gives you more control over which models to use.

## Have your first conversation

You have two options:

**From the Web UI:** Open the **Chat** section and start typing. Stella responds using your configured model.

**From Telegram:** Connect a Telegram bot to chat with Stella from your phone. See the [Telegram channel guide](/docs/channels/telegram) for setup instructions.

## Next steps

- [Deploy Stella as a service](/docs/start-here/deployment) so it runs in the background on startup
- [Configure agents, models, and settings](/docs/start-here/configuration) in the Web UI
- [Connect Telegram](/docs/channels/telegram), [QQ](/docs/channels/qq), [Feishu](/docs/channels/feishu), or [WeChat](/docs/channels/weixin) so you can chat from anywhere
- [Set up reminders and scheduled tasks](/docs/guides/scheduling) to let Stella work on its own
- [Browse and install skills](/docs/guides/skills) to extend what Stella can do
- [Explore the API References](/api-references) for the full REST API documentation
