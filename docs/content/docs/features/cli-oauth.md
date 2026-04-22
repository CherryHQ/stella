---
title: CLI OAuth
---

## Overview

The CLI OAuth feature lets agents use `gh` (GitHub CLI) and `lark-cli` directly from
sandbox sessions without manual authentication. Anna handles the OAuth device flow on
the host, stores a versioned token bundle in your personal vault, and injects a fresh
runtime token into each sandbox environment automatically.

## Prerequisites

An admin must configure the relevant auth plugin with application credentials before
any user can connect:

- **GitHub**: The admin must set a GitHub OAuth app's client ID and secret in the
  GitHub auth plugin settings.
- **Lark / Feishu**: The admin must set a Lark app ID, app secret, and brand
  (`lark` or `feishu`) in the Lark auth plugin settings.

## Connecting

1. Open the Anna admin panel and navigate to your **Profile** page.
2. Find the **OAuth CLI Credentials** section.
3. Click **Connect** next to the provider you want to link.
4. Anna starts a device flow and displays a verification URL and user code.
5. Open the URL in a browser, enter the code, and authorize.
6. Anna polls for completion. Once authorized, the token bundle is saved to your vault.

You can disconnect at any time by clicking **Disconnect** next to the provider.

## Using the CLIs

After connecting, raw `gh` and `lark-cli` commands work inside agent sandbox sessions
without any additional configuration. Anna prepends a wrapper directory to `PATH` so
that every `gh` or `lark-cli` invocation automatically receives the correct credentials.
For `lark-cli`, Anna injects `LARKSUITE_CLI_USER_ACCESS_TOKEN`,
`LARKSUITE_CLI_APP_ID`, and `LARKSUITE_CLI_BRAND`, so no per-session `config init`
is required.

Example (issued by the agent inside a bash tool call):

```sh
gh issue list --repo owner/repo
lark-cli message send --chat-id <id> --text "Hello"
```

## Known limitations

### Lark token expiry

Lark user access tokens expire after approximately **2 hours**. Anna refreshes them
at session start only. If an agent session outlives the token, `lark-cli` calls will
fail with an authentication error. Starting a new Anna session will pick up a freshly
refreshed token automatically.

### Restart loses in-flight device flows

Pending device flows (started but not yet authorized) are held in memory. An Anna
process restart discards them. If Anna restarts while you are completing authorization
in a browser, you will need to start the flow again from the profile page.

## Security model

OAuth token bundles (`GH_OAUTH`, `LARK_CLI_OAUTH`) are stored encrypted at rest in
your vault using the same age-based encryption as other vault entries. They are
treated as host-only data: the raw JSON bundles are never forwarded into the sandbox
process environment. Only the derived runtime token (e.g., `GH_TOKEN` for GitHub) is
injected, so sandbox processes never have access to refresh credentials or OAuth app
secrets.
