---
title: Secrets and Keys
---

## What the vault does

Stella's vault stores API keys, tokens, and other secrets encrypted at rest. A user secret has three parts: **name, value, and scope**.

Delivery is automatic. When an agent session starts, Stella puts every matching user secret into the sandbox environment using the secret name as the environment variable name. If a CLI expects `GITHUB_TOKEN`, name the secret `GITHUB_TOKEN`.

System-managed secrets such as OAuth credentials are internal to Stella. They are not exposed as sandbox environment variables.

## Setup

Before using the vault, generate a master encryption key and provide it to Stella.

### 1. Generate a master key

```bash
stellad vault keygen
```

Copy the line starting with `AGE-SECRET-KEY-1`. `stellad vault keygen --help` is the source of truth for bootstrap flags.

### 2. Start Stella with the key

```bash
export STELLA_VAULT_KEY="AGE-SECRET-KEY-1..."
stellad server
```

> **Back up your master key.** If it is lost, all stored secrets become permanently unrecoverable. Store it in a password manager or another secure location.

That is all the setup you need. Stella automatically handles per-user encryption when new users are added.

## Add a secret

### From the Web UI

1. Open the Web UI and go to Credentials.
2. In the Vault section, choose the secret's scope.
3. Enter the secret name, exactly matching the environment variable the tool expects.
4. Enter the value and save.

Secret values are write-only in the Web UI. You can list names and metadata later, but not reveal the plaintext value.

### Secret scopes

| Scope                              | Who can set it | Available as environment variables in |
| ---------------------------------- | -------------- | ------------------------------------- |
| My credentials · all agents        | You            | All of your agents' sessions          |
| My credentials · specific agent    | You            | The selected agent's sessions         |
| Admin credentials · system-wide    | Admins         | Every user's agent sessions           |
| Admin credentials · specific agent | Admins         | Sessions for the selected agent       |

When several scopes define the same name, Stella uses this precedence: your agent-specific secret, your all-agent secret, admin agent-specific secret, then admin system-wide secret.

### From a chat session

You can also store secrets during a conversation. When Stella asks for a key, send it with the config command:

```text
/config SECRET_NAME your-secret-value
```

This writes the value directly to your vault without exposing it in the conversation history. Stella resumes your task after the secret is stored.

You can also ask Stella to manage secrets:

- **"List my vault secrets."**
- **"Delete the GITHUB_TOKEN secret."**

## Use secrets in sessions

Secrets are available automatically in matching sandbox sessions. A secret named `GITHUB_TOKEN` is available as `$GITHUB_TOKEN` to bash commands and third-party CLIs.

If a stored secret has the same name as an environment variable derived from a connected account, the stored secret wins.

You do not bind secrets to agents or projects separately. The scope is the only targeting control.

Group sessions do not receive vault secrets.

Secrets are loaded fresh for each session. If you add or update a secret, the change takes effect on the next session.

Agent sessions do not receive a Stella API bearer token. Agents use built-in tools for Stella capabilities instead of calling the HTTP API from the sandbox.

## Secret name rules

Secret names must follow these rules:

- Uppercase letters, digits, and underscores only, for example `MY_API_KEY`
- Must start with a letter
- Maximum 128 characters
- Cannot use system-managed credential names such as `STELLA_TOKEN` or `GH_OAUTH`
- Cannot start with reserved prefixes such as `STELLA_`, `OAUTH_`, `MCP_TOKEN_`, `LD_`, or `DYLD_`
- Cannot use execution-hook names such as `BASH_ENV`, `ENV`, `PROMPT_COMMAND`, `GIT_SSH_COMMAND`, `NODE_OPTIONS`, or `PYTHONSTARTUP`

## Tips

- **Name secrets after the tool's environment variable.** If a tool documents `OPENAI_API_KEY`, use that exact name.
- **Scope narrowly.** Put service-specific keys on the one agent that needs them.
- **Secrets persist across restarts.** You only need to set them once.
- **Rotate by overwriting.** Save a new value with the same name and scope to replace the old value.
