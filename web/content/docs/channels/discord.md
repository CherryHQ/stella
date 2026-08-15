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
5. In Stella's Web UI, open **Channels**, create a Discord channel, paste the bot token, turn on **Allow server channels** if the bot should serve the servers it joined, then enable it.
6. Restart `stellad server` if the channel does not start automatically.

Linked users can use their selected agent in direct messages. By default, an unlinked Discord user can request account linking but cannot reach an agent. You can optionally enable persistent guest direct messages as described below. Bind the Discord channel instance to an agent before using it in server channels so Stella can add that agent to each encountered channel's group routing.

## Optional guest direct messages

Set `allow_unlinked_dm` to `true` to let unlinked Discord users chat with the agent bound to this channel instance. This option is off by default and also requires `allow_dm: true`, an enabled channel, and a bound agent. Guests cannot choose another agent.

Guest conversation history persists across direct messages and is compacted when it grows, but the restricted guest session has no profile, reflection, tools, skills, files, workspace, plugins, or delegation. Guests can use only `/link`, `/help`, `/new`, `/compact`, and `/abort`. Linking a Stella account does not merge the earlier guest history into that account's history.

Guest chat traffic is limited per guest, each channel has a durable guest cap, and inactive guest identities and their sessions are deleted by the daily retention job. Valid account-link codes are handled before guest admission and do not consume the guest chat budget. Throttled attempts still refresh the guest's activity timestamp. Configure these limits in the Web UI. Administrators can inspect and delete guest sessions through the normal session management surfaces.

> **Warning:** Enabling guest direct messages makes your model available to the public and can create unexpected provider costs and security risk even with these limits. Use a dedicated guest-safe agent, and make sure its base prompt contains no secrets.

## Using the bot

Send the bot a direct message, mention it in a server channel, or reply to one of its server messages. Stella immediately replies with a temporary progress message, updates it with generated text and tool activity, and keeps Discord's typing indicator active until the answer is ready. Long answers and answers with attachments replace the preview with normal Discord messages when complete.

In forum posts and other threads, a later mention also includes the post starter and up to 20 recent earlier messages, including messages sent before the bot was mentioned. Thread context is capped at 24 KiB; the oldest non-starter messages are omitted when needed.

With **Allow server channels** off, every server message is ignored. By default, server messages that do not mention the bot are ignored as standalone turns and do not invoke an agent. Every member who can access a channel the bot can read may mention it, so use Discord channel and role permissions for access control, and invite the bot only to servers you trust. Agent output cannot trigger Discord mentions such as `@everyone`.

The bot supports `/start`, `/help`, `/new`, `/compact`, `/abort`, `/whoami`, and `/link` in direct messages. Discord receives commands as normal text messages; you do not need to register Discord application commands.

Images and files up to 25 MiB are downloaded from Discord's attachment service and saved to your private assets directory when storage is available. Agent-created images and files are uploaded back to Discord.

## Link your account

In Stella's user or agent channel settings, generate a Discord link code. Send `/link <code>` to the bot from your Discord account. Stella then uses that identity for direct-message routing and notifications.

For an explicit notification target, use a real Discord channel ID. Enable Discord Developer Mode, right-click the channel, and select **Copy Channel ID**.

## Configuration reference

| Field                            | Description                                              | Default    |
| -------------------------------- | -------------------------------------------------------- | ---------- |
| `token`                          | Discord bot token                                        | (required) |
| `allow_group`                    | Accept messages from server channels the bot can read    | `false`    |
| `allow_dm`                       | Accept account linking and linked-user DMs               | `true`     |
| `allow_unlinked_dm`              | Allow restricted guest DMs on the bound agent            | `false`    |
| `guest_message_limit_per_minute` | Maximum messages and commands per guest each minute      | `10`       |
| `guest_max_per_channel`          | Maximum durable guest identities for the channel         | `1000`     |
| `guest_retention_days`           | Delete guests and sessions after this many inactive days | `30`       |
| `require_mention`                | Require a bot mention in server channels                 | `true`     |

## Troubleshooting

**The bot connects but cannot read messages:** Enable **Message Content Intent** in the Developer Portal, then restart Stella.

**The bot cannot reply or upload files:** Check its channel overrides as well as its server role. It needs View Channels, Send Messages, Read Message History, and Attach Files.

**The bot ignores direct messages:** Set `allow_dm` to `true`. To accept unlinked users as restricted guests, also bind a dedicated guest-safe agent and set `allow_unlinked_dm` to `true`.

**The bot does not respond in a server channel:** Turn on **Allow server channels**, then mention the bot. To allow semantic routing of messages without a mention, set `require_mention` to `false` and make sure an eligible group-routing model is configured.

**The channel reports an authentication error:** Reset the token in the Developer Portal, replace it in Stella, and never paste the token into chat or logs.
