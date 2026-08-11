---
title: OAuth Connections
---

Stella OAuth connections provide user tokens to tools that explicitly declare an `oauth_provider`. The built-in `gh` integration uses this path; manifest tools can declare other configured providers.

## What OAuth connections do

When you connect a service, Stella securely stores its access token and injects it only into enabled tools that declare that provider. This means Stella can:

- Create GitHub issues, open pull requests, and query repositories using `gh`
- Use Feishu/Lark documents, calendars, tasks, and messages through `lark-cli`
- Authorize a custom manifest tool that explicitly declares an OAuth provider

OAuth provider scope settings and token refresh apply only to those consumers.

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

## Connecting Feishu or Lark

An administrator first configures the Feishu or Lark OAuth app under **Admin Console → Deployment resources → System Credentials**. Users then connect it from chat or their **Credentials** page using the same browser authorization flow as GitHub. Do not paste an App Secret into chat and do not run `lark-cli config init` inside an agent sandbox.

The built-in `lark-cli` tool targets Feishu by default. Lark deployments can bind the tool manifest to the `lark` provider instead.

## Admin: managing providers

Admins manage each OAuth provider from the provider's detail panel on the **Credentials** page.

### App credentials

Set the provider's **Client ID** and **Client Secret**. Saving new credentials marks already-connected users as needing to reconnect, because a token issued by the old app no longer matches.

### Scopes

**Minimum scopes** are the permissions every connection requests. They are a floor, not a ceiling: a user whose tool needs more can request additional scopes and re-authorize, and Stella unions those into only that user's own desired set. The OAuth app configuration and the provider's consent screen decide what a user can actually grant — Stella does not add a second gate in front of them.

Admins override the floor with the scope editor:

- The checklist always shows every built-in scope. Without an override they start selected; afterward, the checked state matches the saved configuration. Uncheck a scope to remove it from the next authorization request.
- Scopes are grouped by namespace prefix (for example `im:`, `docs:`), collapsed by default, and searchable.
- **Restore defaults** selects the built-in list and removes custom scopes from the draft.
- Use the input below the checklist to add scopes that are not in the built-in list. Stella splits pasted lines, commas, and spaces and removes duplicates.

Saving applies the checked scopes as the floor for every user. Changing the floor does **not** silently widen already-issued tokens, but affected users are asked to reconnect. Lowering the floor does not take away scopes a user already asked for; their desired set only shrinks when they re-authorize. To remove a granted permission for real, disconnect and revoke the grant on the provider's side.

Built-in defaults are chosen per provider. Lark and Feishu ship the full lark-cli capability surface, so a single authorization covers every documented command; GitHub and X ship the scopes their tools need. Trim the floor when your deployment needs less — a smaller first-consent screen is easier for users to accept, and a scope only a few people need can come from their own incremental request.

Every scope in the floor must also be enabled in the provider's app console. A scope the app does not offer is simply left out of the grant, which shows up as a **Reconnect needed** state that reconnecting cannot clear; remove it from the floor or enable it in the console.

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

## Troubleshooting

### Authorization interrupted by a restart

If Stella restarts during a provider authorization, start that connection again.

### GitHub commands not working

Make sure your GitHub account is connected by checking the Credentials page in the Web UI. If the status shows disconnected, connect again. GitHub tokens do not expire, so once connected they should work indefinitely.
