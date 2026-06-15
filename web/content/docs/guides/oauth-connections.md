---
title: OAuth Connections
---

You can connect your GitHub and Feishu/Lark accounts so Stella can use those services on your behalf. Once connected, Stella runs `gh` and `lark-cli` commands directly -- no manual login needed in each session.

## What OAuth connections do

When you connect a service, Stella securely stores an access token and injects it into every agent session. This means Stella can:

- Create GitHub issues, open pull requests, and query repositories using `gh`
- Send Feishu/Lark messages, query calendars, and manage documents using `lark-cli`

You authorize once, and it works from that point on.

## Connecting GitHub

GitHub works out of the box -- no admin setup needed.

### Option 1: Ask Stella in chat

1. Tell Stella: "Connect my GitHub account."
2. Stella starts a device authorization flow and gives you a URL and a one-time code.
3. Open the URL in your browser, enter the code, and authorize.
4. Stella detects the authorization and confirms the connection.

### Option 2: Use the Web UI

1. Open the Web UI and go to your **Credentials** page.
2. Find **OAuth CLI Credentials** and click **Connect** next to GitHub.
3. Stella shows you a URL and a one-time code.
4. Open the URL in your browser, enter the code, and authorize.
5. Once authorized, the status updates to connected.

You can disconnect at any time by clicking **Disconnect** on the Credentials page.

## Connecting Feishu / Lark

Feishu and Lark require an admin to configure app credentials before users can connect.

Logging in with Feishu only authenticates you -- it does not connect Feishu tools. After login, connect the tool credential once as below. You can check connection status on the **Credentials** page in the Web UI, where the Feishu provider shows a "connect to enable …" prompt naming the tools that depend on it.

### Connecting

To connect Feishu/Lark (or GitHub) tools:

#### Admin setup (one-time)

An admin must configure the Lark CLI plugin in the Web UI with:

- **App ID** and **App Secret** from your Feishu/Lark app
- **Brand** -- choose `feishu` for domestic Feishu or `lark` for international Lark

Once configured, all users can connect their accounts.

#### Connecting your account

Follow the same steps as GitHub -- either ask Stella in chat or use the Credentials page in the Web UI. Stella walks you through the same device authorization flow.

## Using connected services

After connecting, `gh` and `lark-cli` commands just work in agent sessions. You can ask Stella things like:

- "List open issues on my repo"
- "Create a pull request with these changes"
- "Send a message to the engineering channel on Feishu"

Stella handles authentication automatically behind the scenes.

## Troubleshooting

### Feishu/Lark token expired

Feishu and Lark tokens expire after approximately 2 hours. Stella refreshes them automatically when they are close to expiring. If you get an authentication error mid-session, reconnect through the Credentials page or ask Stella to reconnect -- the next message will use the fresh token.

### Authorization interrupted by a restart

If Stella restarts while you are in the middle of authorizing (you have the URL but have not completed the browser step), the flow is lost. Start the connection process again -- it only takes a moment.

### GitHub commands not working

Make sure your GitHub account is connected by checking the Credentials page in the Web UI. If the status shows disconnected, connect again. GitHub tokens do not expire, so once connected they should work indefinitely.
