---
title: Secrets and Keys
---

## What the Vault Does

Stella's vault stores your API keys, tokens, and other secrets securely. Values are encrypted at rest. New secrets are not injected into agent sessions unless you explicitly bind them to agents or projects, or mark them as always injected.

## Setup

Before using the vault, you need to generate a master encryption key and provide it to Stella.

### 1. Generate a Master Key

```bash
stella vault keygen
```

This prints a secret key. Copy the line starting with `AGE-SECRET-KEY-1`.

### 2. Start Stella with the Key

```bash
export STELLA_VAULT_KEY="AGE-SECRET-KEY-1..."
stellad server
```

> **Back up your master key.** If it is lost, all stored secrets become permanently unrecoverable. Store it in a password manager or another secure location.

That is all the setup you need. Stella automatically handles per-user encryption when new users are added.

## Adding Secrets

### From the Web UI

1. Open the Web UI and go to your Credentials page.
2. In the Vault section, choose where the secret applies.
3. Enter a secret name (for example, `GITHUB_TOKEN`) and its value.
4. Click Save.

Secret values are write-only — once saved, the plaintext is never shown in the Web UI again. You can see the list of secret names, but not their values.

### Secret scopes

Secrets can apply at four levels:

| Scope                              | Who can set it | Available when                   |
| ---------------------------------- | -------------- | -------------------------------- |
| My credentials · all agents        | You            | You use any agent                |
| My credentials · specific agent    | You            | You use the selected agent       |
| Admin credentials · system-wide    | Admins         | Any user uses any agent          |
| Admin credentials · specific agent | Admins         | Any user uses the selected agent |

When several scopes define the same name, Stella lets the most specific personal secret win: your agent-specific secret overrides your all-agent secret, which overrides the admin agent-specific secret, which overrides the admin system-wide secret.

### From a Chat Session

You can also store secrets directly in conversation. When Stella needs a key she does not have, she will ask you to provide it. Send it using the config command:

```
/config SECRET_NAME your-secret-value
```

This writes the value directly to your vault without exposing it to the conversation history. Stella resumes your task automatically after the secret is stored.

You can also ask Stella to manage your secrets:

- **"List my vault secrets."**
- **"Delete the GITHUB_TOKEN secret."**

## Using Secrets in Sessions

When an agent session starts, Stella decrypts only secrets whose injection settings match that session: always-injected secrets, secrets bound to the current agent, or secrets bound to the current project. A bound secret named `GITHUB_TOKEN` is available as `$GITHUB_TOKEN` inside the session.

Secrets are loaded fresh for each session. If you add or update a secret, the change takes effect on the next session.

### Auto-Generated Token

Stella automatically creates a `STELLA_TOKEN` for each user. This token is used to authenticate CLI commands and API requests from within agent sessions. You do not need to create it manually.

## Secret Name Rules

Secret names must follow these rules:

- Uppercase letters, digits, and underscores only (for example, `MY_API_KEY`)
- Must start with a letter
- Maximum 128 characters
- Cannot start with reserved prefixes like `STELLA_` (except the auto-generated `STELLA_TOKEN`)

## Tips

- **One secret per service.** Store `GITHUB_TOKEN`, `OPENAI_API_KEY`, and similar keys as individual vault entries, then bind each one only where it is needed.
- **Secrets persist across restarts.** You only need to set them once.
- **Use the Web UI to audit.** The Credentials page shows all secret names, so you can see what is stored without exposing values.
- **Rotate secrets by overwriting.** To update a secret, save a new value with the same name — it replaces the old one.
