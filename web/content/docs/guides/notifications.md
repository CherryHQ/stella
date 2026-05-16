---
title: Notifications
---

Stella can send you messages proactively -- you do not have to ask. When a scheduled job finishes, a background task needs your attention, or a monitored service goes down, Stella pushes a notification to the channels you have connected.

## How notifications work

Notifications are delivered to your connected messaging channels. If you have Telegram connected, you get a Telegram message. If you have multiple channels connected, Stella sends to all of them.

You do not need to be in an active conversation. Notifications arrive whether you are chatting with Stella or not.

## Where you receive them

Stella delivers notifications to any channel you have connected:

- **Telegram** -- direct message to your Telegram account
- **QQ** -- message to your QQ account
- **Feishu / Lark** -- message to your Feishu or Lark account
- **WeChat** -- message to your WeChat account

The notification goes to the account you used to connect with Stella. If you started chatting with Stella on Telegram, that Telegram account receives your notifications.

## Configuring notifications

Channel configuration is managed in the admin panel. Each channel you set up for conversations also serves as a notification target. Once a channel is connected and running, it can receive notifications automatically.

To set up a channel:

1. Open the admin panel.
2. Go to the channel settings (for example, Telegram).
3. Configure the channel with the required credentials (bot token, etc.).
4. Start using the channel -- once Stella knows your account, notifications route to you.

## What triggers notifications

Notifications come from several sources:

- **Scheduled jobs** -- when a recurring job finishes (daily summaries, periodic checks, etc.), Stella sends you the result.
- **Background tasks** -- when a task completes, fails, or needs your input (a question or review request), you get a notification.
- **Heartbeat monitoring** -- if you have uptime checks configured, Stella alerts you when a service goes down or recovers.
- **Agent-initiated** -- during a conversation, Stella may send you a notification to a specific channel if you ask it to (for example, "Send this summary to my Telegram").

## Silent notifications

Some notifications are sent silently -- they appear in your chat history without triggering a sound or vibration. Stella decides when to use silent mode based on the urgency of the message. Scheduled job results, for example, typically arrive silently.
