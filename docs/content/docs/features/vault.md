---
title: Vault
---

## Overview

The vault provides per-user encrypted secret storage. Secrets are encrypted at rest using [age](https://age-encryption.org) and injected as environment variables into every sandbox session. No secret value is ever stored or transmitted in plaintext.

## Setup

The vault requires a single master key, provided via the `ANNA_VAULT_KEY` environment variable. When this variable is not set, the vault endpoints return `503` and secrets are not injected.

### Generate a master key

```bash
age-keygen
```

This prints a public key line and a secret key line. Copy the secret key line (beginning with `AGE-SECRET-KEY-1`).

### Configure

Set the environment variable before starting anna:

```bash
export ANNA_VAULT_KEY="AGE-SECRET-KEY-1..."
anna server
```

> **Back up the master key.** If it is lost, all stored secrets become permanently unrecoverable. Store it in a password manager or secrets manager.

### Startup provisioning

On startup anna automatically generates an age keypair for every user that does not have one yet. No manual provisioning step is required when adding new users.

## Using the vault

Open the admin panel and navigate to your profile page. The **Vault** section lets you:

- **Add** a secret by entering a name and value, then clicking Save.
- **Delete** a secret by clicking the trash icon next to its name.
- **List** your secrets (names only — values are never shown in the UI).

Secret values are write-only from the UI. Once saved, the plaintext is not retrievable through the admin panel.

## Secret injection

When a sandbox session starts, all vault secrets belonging to the authenticated user are decrypted and injected as environment variables. A secret named `GITHUB_TOKEN` is available as `$GITHUB_TOKEN` inside the sandbox.

Secrets are loaded fresh for each session. Adding or updating a secret takes effect on the next sandbox session.

## Name restrictions

Vault entry names follow the same rules as POSIX environment variable names, with additional reserved-name checks:

- Must match `^[A-Z][A-Z0-9_]{0,127}$` — uppercase letters, digits, and underscores; starts with a letter; maximum 128 characters.
- Must not start with `ANNA_`, `LC_`, or `XDG_`.
- Must not equal or start with `PATH_`, `HOME_`, `USER_`, `SHELL_`, `LANG_`, `TERM_`, or `TMPDIR_`.

## Security model

Anna uses **envelope encryption**:

1. At startup a master X25519 keypair is loaded from `ANNA_VAULT_KEY`.
2. Each user receives their own X25519 keypair. The user's private key is encrypted with the master public key and stored in the database.
3. When a secret is stored, it is encrypted with the **user's** public key.
4. When secrets are loaded for a sandbox session, the master identity decrypts the user's private key, then that private key decrypts each secret.

Key properties of this design:

- **Write-only API**: the admin panel can store and delete secrets but cannot retrieve plaintext values.
- **Per-user isolation**: one user's secrets cannot be decrypted without that user's private key.
- **Master key controls access**: rotating or revoking the master key prevents further decryption.
- **No plaintext at rest**: all values in the database are age-encrypted ciphertext.
