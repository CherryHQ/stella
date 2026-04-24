---
title: CLI OAuth
---

## Overview

The CLI OAuth feature lets agents use `gh` (GitHub CLI) and `lark-cli` directly from
sandbox sessions without manual authentication. Anna handles the OAuth device flow on
the host, stores a versioned token bundle in your personal vault, and injects a fresh
runtime token into each sandbox environment automatically.

## Prerequisites

GitHub works out of the box. Anna uses the public GitHub CLI OAuth device-flow app,
so users can connect their GitHub account without any admin-side plugin settings.

Feishu / Lark still requires an admin to configure an app ID, app secret, and
brand in the Lark CLI plugin settings before users can connect. The brand selects
both the OAuth provider and the injected `LARKSUITE_CLI_BRAND` value. The default
brand is `feishu`; choose `lark` for international Lark. Leave `redirect_url`
empty unless you need an override: Anna derives it from the current Admin UI origin,
such as `http://localhost:25678/api/auth/profile/oauth/feishu/callback`.

## Connecting

### Via the credentials tool (channel sessions)

Inside any Anna channel session, ask the agent to connect a provider. It will call the
`credentials` tool, which starts a device flow and returns a verification URL and user
code. Open the URL in a browser, enter the code, and authorize. The agent polls for
completion automatically and resumes your task once connected.

### Via the admin panel (web UI)

1. Open the Anna admin panel and navigate to your **Credentials** page.
2. Find the **OAuth CLI Credentials** section.
3. Click **Connect** next to the provider you want to link.
4. Anna starts a device flow and displays a verification URL and user code.
5. Open the URL in a browser, enter the code, and authorize.
6. Anna polls for completion. Once authorized, the token bundle is saved to your vault.

You can disconnect at any time by clicking **Disconnect** next to the provider, or by
asking the agent to run `credentials disconnect` for the relevant provider.

## Choosing Feishu or Lark for `lark-cli`

`lark-cli` uses the `brand` field in the Lark CLI plugin config as the single
selector:

- `brand: feishu` uses the Feishu OAuth endpoints and injects `LARKSUITE_CLI_BRAND=feishu`.
- `brand: lark` uses the international Lark OAuth endpoints and injects `LARKSUITE_CLI_BRAND=lark`.

You do not need to duplicate that choice in `$ANNA_HOME/plugins.yaml`. The built-in
manifest resolves `oauth_provider` from the plugin config field:

```yaml
oauth_provider_config_field: brand
oauth_provider_choices: [lark, feishu]
session_env:
  - env_var: LARKSUITE_CLI_BRAND
    source: oauth.brand
```

Only override `tool/lark-cli` in `$ANNA_HOME/plugins.yaml` if you need to change the
binary or session environment declarations themselves.

## Using the CLIs

After connecting, raw `gh` and `lark-cli` commands work inside agent sandbox sessions
without any additional configuration. Anna injects the required auth env for each
session and prepends `$ANNA_HOME/bin` to `PATH`, so `gh` and `lark-cli` resolve
straight to Anna-managed binaries. For `lark-cli`, Anna injects
`LARKSUITE_CLI_USER_ACCESS_TOKEN`, `LARKSUITE_CLI_APP_ID`, and
`LARKSUITE_CLI_BRAND`, so no per-session `config init` is required.

Example (issued by the agent inside a bash tool call):

```sh
gh issue list --repo owner/repo
lark-cli message send --chat-id <id> --text "Hello"
```

## Known limitations

### Feishu/Lark token expiry

Feishu and Lark user access tokens expire after approximately **2 hours**. Anna
refreshes them at session start only. If an agent session outlives the token,
`lark-cli` calls will fail with an authentication error. Starting a new Anna session
will pick up a freshly refreshed token automatically.

### Restart loses in-flight device flows

Pending device flows (started but not yet authorized) are held in memory. An Anna
process restart discards them. If Anna restarts while you are completing authorization
in a browser, you will need to start the flow again (via the `credentials` tool or the
Credentials page).

## Security model

OAuth token bundles (`GH_OAUTH`, `FEISHU_CLI_OAUTH`, and `LARK_CLI_OAUTH` when the
`lark-cli` brand is `lark`) are stored encrypted at rest in your vault
using the same age-based encryption as other vault entries. They are treated as
host-only data: the raw JSON bundles are never forwarded into the sandbox process
environment. Only the derived runtime token (for example, `GH_TOKEN` for GitHub) is
injected, so sandbox processes never receive refresh credentials or OAuth app secrets.
