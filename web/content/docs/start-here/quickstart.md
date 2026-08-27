---
title: Quick Start
---

Get Stella running and have your first AI conversation in under five minutes.

## Install

**Homebrew (macOS and Linux):**

```bash
brew install CherryHQ/tap/stella
```

**From source:**

```bash
git clone https://github.com/CherryHQ/stella.git
cd stella && mise run setup && mise run build
# the binary lands in dist/bin/stellad
```

`go install` is not supported. The binary embeds generated API code, the built
Web UI, and the bundled runtimes; none of those are in version control, so a
build from the module alone does not compile.

**Binary download:**

Grab the latest binary from [Releases](https://github.com/CherryHQ/stella/releases), or self-update an existing install with:

```bash
stellad upgrade
```

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
3. Click **Add Provider**, select its type, and enter its API key and base URL when required.
4. Save the provider, then select one of its models for the seeded **stella** agent on the **Agents** page.

Provider credentials and model selection are managed in the Web UI.

## Have your first conversation

You have two options:

**From the Web UI:** Open the **Chat** section and start typing. Stella responds using your configured model.

**From Telegram:** Connect a Telegram bot to chat with Stella from your phone. See the [Telegram channel guide](/docs/channels/telegram) for setup instructions.

## Next steps

- [Deploy Stella as a service](/docs/start-here/deployment) so it runs in the background on startup
- [Configure agents, models, and settings](/docs/start-here/configuration) in the Web UI
- [Connect Telegram](/docs/channels/telegram), [Discord](/docs/channels/discord), [QQ](/docs/channels/qq), [Feishu](/docs/channels/feishu), [DingTalk](/docs/channels/dingtalk), or [WeChat](/docs/channels/weixin) so you can chat from anywhere
- [Set up reminders and scheduled tasks](/docs/guides/scheduling) to let Stella work on its own
- [Browse and install skills](/docs/guides/skills) to extend what Stella can do
- [Explore the API References](/api-references) for the full REST API documentation
