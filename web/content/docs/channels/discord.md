---
title: Discord Bot
---

Connect Stella to Discord through a bot Gateway connection. No webhook or public URL is required. The bot supports direct messages, server channels, replies, images, files, commands, and proactive notifications.

## Prerequisites

- A running Stella server (`stellad server`)
- At least one AI provider configured in the Web UI
- Permission to create a Discord application and invite its bot to a server

## Setup

1. Open the [Discord Developer Portal](https://discord.com/developers/applications) and create an application.
2. Open **Bot**, create the bot if needed, and copy its token. Treat the token as a secret.
3. Under **Privileged Gateway Intents**, enable **Message Content Intent**. If only your organization should install the bot, turn off **Public Bot**.
4. In **OAuth2 → URL Generator**, select the `bot` scope. Grant **View Channels**, **Send Messages**, **Read Message History**, and **Attach Files**, then use the generated URL to invite the bot.
5. Enable Discord Developer Mode, right-click each server you trust Stella to serve, and select **Copy Server ID**.
6. In Stella's Web UI, open **Channels**, create a Discord channel, paste the bot token and the trusted server IDs into **Allowed Guild IDs**, then enable it. Separate multiple IDs with commas.
7. Restart `stellad server` if the channel does not start automatically.

Linked users can use their selected agent in direct messages. An unlinked Discord user can request account linking but cannot reach an agent, session, or tools. Bind the Discord channel instance to an agent before using it in server channels so Stella can add that agent to each encountered channel's group routing.

## Using the bot

Send the bot a direct message or mention it in an allowed server channel. Messages from servers not listed in **Allowed Guild IDs** are ignored. By default, server messages that do not mention the bot are also ignored before they enter shared history or invoke an agent. Every member who can access an allowed channel can mention the bot, so use Discord channel and role permissions for access control. Agent output cannot trigger Discord mentions such as `@everyone`.

The bot supports `/start`, `/help`, `/new`, `/compact`, `/abort`, `/agent`, `/whoami`, and `/link` in direct messages. `/model` and server-channel `/agent` are not yet supported. Discord receives commands as normal text messages; you do not need to register Discord application commands.

Images and files up to 25 MiB are downloaded from Discord's attachment service and saved to your private assets directory when storage is available. Agent-created images and files are uploaded back to Discord.

## Link your account

In Stella's user or agent channel settings, generate a Discord link code. Send `/link <code>` to the bot from your Discord account. Stella then uses that identity for direct-message routing and notifications.

For an explicit notification target, use a real Discord channel ID. Enable Discord Developer Mode, right-click the channel, and select **Copy Channel ID**.

## Configuration reference

| Field               | Description                                      | Default    |
| ------------------- | ------------------------------------------------ | ---------- |
| `token`             | Discord bot token                                | (required) |
| `allowed_guild_ids` | Comma-separated server IDs allowed to use Stella | (none)     |
| `allow_dm`          | Accept account linking and linked-user DMs       | `true`     |
| `require_mention`   | Require a bot mention in server channels         | `true`     |

## Troubleshooting

**The bot connects but cannot read messages:** Enable **Message Content Intent** in the Developer Portal, then restart Stella.

**The bot cannot reply or upload files:** Check its channel overrides as well as its server role. It needs View Channels, Send Messages, Read Message History, and Attach Files.

**The bot ignores direct messages:** Set `allow_dm` to `true`. Unlinked users can only submit an account link code; they cannot invoke an agent, create a session, or access memory and tools.

**The bot does not respond in a server channel:** Add that server's ID to **Allowed Guild IDs**, then mention the bot. To allow semantic routing of messages without a mention, set `require_mention` to `false` and make sure an eligible group-routing model is configured.

**The channel reports an authentication error:** Reset the token in the Developer Portal, replace it in Stella, and never paste the token into chat or logs.
