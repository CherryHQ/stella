---
title: Deployment
---

Two deployment methods: **binary** (direct install) and **Docker**.

## Binary

### From Release

Download a pre-built binary from [GitHub Releases](https://github.com/vaayne/anna/releases). Binaries are available for linux, macOS, and Windows on amd64/arm64.

```bash
# Example: Linux amd64
curl -LO https://github.com/vaayne/anna/releases/latest/download/anna_linux_amd64.tar.gz
tar xzf anna_linux_amd64.tar.gz
chmod +x anna
sudo mv anna /usr/local/bin/
```

### From Source

```bash
go install github.com/vaayne/anna@latest
# or
git clone https://github.com/vaayne/anna.git
cd anna && go build -o anna .
```

### Running

Run the onboard command to open the admin panel and configure anna (providers, channels, agents, etc.):

```bash
anna onboard
```

This starts a local web UI where you set up API keys, channels, and agent profiles. All configuration is stored in `~/.anna/anna.db` -- no manual config files needed.

Start the gateway daemon:

```bash
anna gateway
```

To serve the admin panel alongside the gateway (for runtime config changes):

```bash
anna gateway --admin-port 8080
```

Or use the interactive CLI:

```bash
anna chat
```

### Version And Self-Upgrade

```bash
anna version
anna upgrade
anna upgrade --install-dir "$HOME/.local/bin"
```

`anna upgrade` fetches the latest stable release from GitHub, downloads the matching archive for the current OS/architecture, and installs the binary into `$HOME/.local/bin` by default.

### Systemd Service (Linux)

```ini
# /etc/systemd/system/anna.service
[Unit]
Description=anna gateway
After=network.target

[Service]
Type=simple
User=anna
WorkingDirectory=/home/anna
ExecStart=/usr/local/bin/anna gateway --admin-port 8080
Restart=on-failure
RestartSec=5

# API keys — all other config lives in the database
Environment=ANTHROPIC_API_KEY=sk-...
Environment=ANNA_HOME=/home/anna/.anna

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable --now anna
```

All configuration (channels, agents, scheduler jobs) is stored in `anna.db`. Use `anna onboard` or the admin panel to manage it.

## Docker

Images are published to `ghcr.io/vaayne/anna` for `linux/amd64` and `linux/arm64`.

### Tags

| Tag            | Description           |
| -------------- | --------------------- |
| `latest`       | Latest stable release |
| `v1.2.3`       | Specific version      |
| `sha-<commit>` | Specific commit       |

### Quick Start

First, run onboard to configure anna:

```bash
docker run -it --rm \
  -v ~/.anna:/home/nonroot/.anna \
  -p 8080:8080 \
  ghcr.io/vaayne/anna:latest \
  anna onboard
```

Then start the gateway:

```bash
docker run -d \
  --name anna \
  -v ~/.anna:/home/nonroot/.anna \
  -e ANTHROPIC_API_KEY=sk-... \
  ghcr.io/vaayne/anna:latest
```

The container runs as `nonroot` user. Mount `~/.anna` to persist the database, skills, and cache. You can set `ANNA_HOME` to change the data directory inside the container.

### Docker Compose

```yaml
# docker-compose.yml
services:
  anna:
    image: ghcr.io/vaayne/anna:latest
    restart: unless-stopped
    volumes:
      - ./anna-data:/home/nonroot/.anna
    environment:
      - ANTHROPIC_API_KEY=sk-...
      # - OPENAI_API_KEY=sk-...
```

```bash
docker compose up -d
```

To run initial setup, use `docker compose exec anna anna onboard` or start the gateway with `--admin-port 8080` and configure via the web UI.

### Build Locally

```bash
# Single platform
docker build -t anna .

# Multi-platform
docker buildx build --platform linux/amd64,linux/arm64 -t anna .
```

## Volumes & Data

All data lives under the anna home directory (`~/.anna` by default, configurable via `ANNA_HOME`).

| Path                                       | Purpose                                         |
| ------------------------------------------ | ------------------------------------------------ |
| `~/.anna/anna.db`                          | Single database (config, memory, scheduler)      |
| `~/.anna/workspaces/{agent-id}/skills/`    | Per-agent installed skills                       |
| `~/.anna/workspaces/{agent-id}/SOUL.md`    | Optional per-agent soul/identity override        |
| `~/.anna/cache/`                           | Model cache (regenerable, safe to delete)        |

The `anna.db` file is the only critical data to back up. It contains all configuration, message history, summaries, and scheduler jobs.

## Environment Variables

Configuration is managed through the admin panel (via `anna onboard` or `--admin-port`). Only a small set of environment variables is supported:

| Variable            | Required | Description                             |
| ------------------- | -------- | --------------------------------------- |
| `ANNA_HOME`         | No       | Anna home directory (default `~/.anna`) |
| `ANTHROPIC_API_KEY` | Yes\*    | Anthropic provider key                  |
| `OPENAI_API_KEY`    | Yes\*    | OpenAI provider key                     |

\* At least one provider key is required. API keys can also be configured via the admin panel.

## Health Check

The gateway logs to stdout. Verify it is running:

```bash
# Binary
anna gateway  # Logs appear in terminal

# Docker
docker logs anna
```
