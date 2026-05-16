---
title: Secrets and Keys
---

## What the Vault Does

Stella's vault stores your API keys, tokens, and other secrets securely. Values are encrypted at rest and automatically available as environment variables inside agent sessions. You never need to paste secrets into chat or hardcode them in scripts — store them once, and Stella uses them whenever needed.

## Setup

Before using the vault, you need to generate a master encryption key and provide it to Stella.

### 1. Generate a Master Key

```bash
age-keygen
```

This prints a public key and a secret key. Copy the line starting with `AGE-SECRET-KEY-1`.

### 2. Start Stella with the Key

```bash
export STELLA_VAULT_KEY="AGE-SECRET-KEY-1..."
stella server
```

> **Back up your master key.** If it is lost, all stored secrets become permanently unrecoverable. Store it in a password manager or another secure location.

That is all the setup you need. Stella automatically handles per-user encryption when new users are added.

## Adding Secrets

### From the Web UI

1. Open the Web UI and go to your Credentials page.
2. In the Vault section, enter a secret name (for example, `GITHUB_TOKEN`) and its value.
3. Click Save.

Secret values are write-only — once saved, the plaintext is never shown in the Web UI again. You can see the list of secret names, but not their values.

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

When an agent session starts, all your vault secrets are decrypted and injected as environment variables. A secret named `GITHUB_TOKEN` is available as `$GITHUB_TOKEN` inside the session.

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

- **One secret per service.** Store `GITHUB_TOKEN`, `OPENAI_API_KEY`, and similar keys as individual vault entries.
- **Secrets persist across restarts.** You only need to set them once.
- **Use the Web UI to audit.** The Credentials page shows all secret names, so you can see what is stored without exposing values.
- **Rotate secrets by overwriting.** To update a secret, save a new value with the same name — it replaces the old one.
