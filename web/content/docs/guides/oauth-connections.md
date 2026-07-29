---
title: OAuth Connections
---

Stella OAuth connections provide user tokens to tools that explicitly declare an `oauth_provider`. The built-in `gh` integration uses this path. Feishu and Lark OAuth providers remain available for other manifest tools, but the built-in `lark-cli` no longer consumes them.

## What OAuth connections do

When you connect a service, Stella securely stores its access token and injects it only into enabled tools that declare that provider. This means Stella can:

- Create GitHub issues, open pull requests, and query repositories using `gh`
- Authorize a custom manifest tool that explicitly declares a Feishu/Lark OAuth provider

OAuth provider scope settings and token refresh apply only to those consumers. They do not configure or authorize lark-cli.

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

## Feishu / Lark OAuth providers

Feishu and Lark providers require an admin to configure app credentials before users can connect. Keep these provider cards if another manifest tool uses them.

Logging in to Stella with Feishu only authenticates your Stella account. Connecting a Feishu/Lark OAuth provider separately authorizes only the tools listed in that provider card's **Required by** hint.

If no tool requires the provider, employees do not need to connect it for lark-cli.

### Admin setup

Configure the provider in the Web UI with:

- **App ID** and **App Secret** from your Feishu/Lark app
- **Brand** -- choose `feishu` for domestic Feishu or `lark` for international Lark

### Connecting an employee

Follow the same steps as GitHub -- either ask Stella in chat or use the Credentials page in the Web UI. Stella walks you through the same device authorization flow.

This flow remains generic Stella OAuth. Do not use it to repair lark-cli authorization.

## Admin: managing providers

Admins manage each OAuth provider from the provider's detail panel on the **Credentials** page.

### App credentials

Set the provider's **Client ID** and **Client Secret** (App ID / App Secret for Feishu/Lark). Saving new credentials marks already-connected users as needing to reconnect, because a token issued by the old app no longer matches.

### Scopes

Each provider ships with a built-in default scope list. Admins can override it with the scope editor:

- The checklist always shows every built-in scope. Without an override they start selected; afterward, the checked state matches the saved configuration. Uncheck a scope to remove it from the next authorization request.
- Scopes are grouped by namespace prefix (for example `im:`, `docs:`), collapsed by default, and searchable.
- **Restore defaults** selects the built-in list and removes custom scopes from the draft.
- Use the input below the checklist to add scopes that are not in the built-in list. Stella splits pasted lines, commas, and spaces and removes duplicates.

Saving applies the checked scopes. Widening the requested scopes does **not** change already-issued tokens: connected users must reconnect to grant the newly requested scopes.

### Reconnect semantics

A connection can be **connected** yet still need action. The provider shows a **Reconnect needed** state when either:

- the app credentials were rotated since the user connected, or
- the requested scopes now include ones the stored token does not hold (the panel lists the concrete missing scopes).

The user reconnects from the same panel; the token health block shows access- and refresh-token expiry so it is clear when a refresh is due.

## Using connected services

After connecting, tools that declare that OAuth provider work in agent sessions. For example:

- "List open issues on my repo"
- "Create a pull request with these changes"

Stella handles authentication automatically behind the scenes.

## How lark-cli authorization differs

For the built-in lark-cli:

1. An admin configures an enabled Feishu/Lark Channel and binds it to the Agent.
2. Stella initializes that Channel app in each employee × Agent workspace.
3. The Agent runs `lark-cli auth status` and starts lark-cli's native device flow when that employee needs user access or additional scopes.
4. lark-cli stores and refreshes the employee token in that isolated workspace.

Do not use the OAuth Credentials page or `/auth feishu` for this flow. Application scopes are managed on the Channel app in the Feishu/Lark developer console; request only the scopes needed by the deployed workflows.

## Troubleshooting

### Feishu/Lark OAuth provider token expired

If a custom tool uses the generic Feishu/Lark OAuth provider, Stella refreshes its token when possible. Reconnect that provider through the Credentials page if refresh fails. This does not affect lark-cli's native token.

### Authorization interrupted by a restart

If Stella restarts during a generic provider authorization, start that connection again. For lark-cli, ask the Agent to start a fresh native device flow only after the previous device code has expired or failed.

### GitHub commands not working

Make sure your GitHub account is connected by checking the Credentials page in the Web UI. If the status shows disconnected, connect again. GitHub tokens do not expire, so once connected they should work indefinitely.
