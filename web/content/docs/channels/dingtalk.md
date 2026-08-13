---
title: DingTalk
description: Connect Stella to DingTalk with a Stream mode enterprise bot.
---

Connect a DingTalk enterprise bot to chat with Stella in direct messages and trusted groups. Stella uses DingTalk Stream mode, so you do not need a public callback URL.

## Create the bot

1. Open the [DingTalk Developer Console](https://open-dev.dingtalk.com/) and create an internal enterprise application.
2. Add the **Robot** capability to the application.
3. Set the robot message receiving mode to **Stream mode**.
4. Publish the application to your organization.
5. Copy the application's **Client ID** and **Client Secret**.

## Connect Stella

1. Open the Stella Web UI and go to **Channels**.
2. Add a **DingTalk** channel.
3. Enter the Client ID and Client Secret.
4. Bind the channel to an enabled agent if you want to use it in groups.
5. Enable and save the channel.
6. Send the bot a direct message in DingTalk.

To link your DingTalk identity to an existing Stella account, create a link code from your Stella profile and send `/link CODE` to the bot.

## Allow a group

Group access is fail-closed. An empty **Allowed group conversation IDs** field disables every group while direct messages continue to work.

1. Add the bot to the DingTalk group and @mention it once.
2. Read the `conversation_id` from the Stella server log entry for the rejected group.
3. Add that ID to **Allowed group conversation IDs**. Separate multiple IDs with commas.
4. Save the channel and @mention the bot again.

DingTalk only delivers group messages addressed to the bot. Keep **Require a mention** enabled unless your DingTalk application has a specific reason to accept another callback form.

## Supported behavior

- Direct text messages
- Group text messages that @mention the bot
- Shared channel commands, including `/link`, `/help`, `/new`, `/compact`, and `/abort`
- Separate Stella identity and memory per linked DingTalk user
- Final text replies over DingTalk's session Webhook
- Notifications while a recently received session Webhook remains valid

## Limitations

- Stella currently handles text messages only. DingTalk images, files, audio, video, rich text, and interactive cards are not yet ingested or sent.
- DingTalk session Webhooks are temporary and kept in memory. After Stella restarts or a Webhook expires, proactive notifications fail until that user or group messages the bot again. Normal replies continue to work because each inbound message carries a fresh Webhook.
- Responses are sent after the agent finishes; DingTalk does not show Stella's token-by-token stream.

## Troubleshooting

**The channel does not start:** verify that the application is published, the credentials belong to the same application, and robot message receiving is set to Stream mode.

**Direct messages get no response:** enable **Allow direct messages**, then check that your DingTalk identity is linked or that guest direct messages are explicitly enabled on a channel bound to a guest-safe agent.

**A group gets no response:** @mention the bot, bind the channel to an enabled agent, and confirm the exact `conversation_id` is in the allowlist.

**Notifications fail after a restart:** send the bot any direct message to refresh its temporary session Webhook, then retry the notification.
