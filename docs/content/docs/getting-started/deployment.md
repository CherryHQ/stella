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

Run the setup command to open the admin panel and configure anna (providers, channels, agents, etc.):

```bash
anna --open
```

This starts a local web UI where you set up API keys, channels, and agent profiles. All configuration is stored in `~/.anna/anna.db` -- no manual config files needed.

Start the daemon:

```bash
anna
```

To serve the admin panel alongside the daemon (for runtime config changes):

```bash
anna --port 8080
anna --host 0.0.0.0 --port 8080
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

A ready-to-use unit file is provided at [`scripts/anna.service`](https://github.com/vaayne/anna/blob/main/scripts/anna.service).

```bash
# Create a dedicated user
sudo useradd --system --no-create-home --shell /bin/false anna
sudo mkdir -p /home/anna/.anna
sudo chown anna:anna /home/anna/.anna

# Install the unit file, substituting the actual anna binary path
sudo sed "s|ANNA_BIN|$(which anna)|g" scripts/anna.service \
  > /etc/systemd/system/anna.service
sudo systemctl daemon-reload
sudo systemctl enable --now anna
sudo journalctl -u anna -f   # follow logs
```

All configuration (channels, agents, scheduler jobs) is stored in `anna.db`. Use `anna --open` or the admin panel to manage it.

### boxsh Sandbox Prerequisites (Linux)

On Linux, Anna uses the managed `boxsh` sandbox by default for the local workspace tools (`bash`, `read`, `write`, `edit`). `boxsh` needs user namespaces and subordinate ID mapping support on the host.

Install the user namespace helpers:

```bash
# Debian / Ubuntu
sudo apt update
sudo apt install uidmap

# Verify helpers exist
which newuidmap
which newgidmap
ls -l /usr/bin/newuidmap /usr/bin/newgidmap
```

Make sure the service user has subordinate UID/GID ranges:

```bash
grep '^anna:' /etc/subuid
grep '^anna:' /etc/subgid
```

Expected shape:

```text
anna:100000:65536
```

If the entries are missing, add them:

```bash
sudo usermod --add-subuids 100000-165535 anna
sudo usermod --add-subgids 100000-165535 anna
```

Verify the kernel allows unprivileged user namespaces:

```bash
sysctl kernel.unprivileged_userns_clone
sysctl user.max_user_namespaces
```

Typical working values are:

```text
kernel.unprivileged_userns_clone = 1
user.max_user_namespaces = 15000
```

Some Ubuntu hosts also block unprivileged user namespaces through AppArmor even when the kernel settings above are enabled. Check:

```bash
sysctl kernel.apparmor_restrict_unprivileged_userns
```

If `boxsh` fails with `sandbox_apply failed: write uid_map: Operation not permitted`, temporarily disable that restriction and retest:

```bash
sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0
```

To persist the Linux prerequisites across reboot:

```bash
sudo tee /etc/sysctl.d/99-anna-boxsh.conf >/dev/null <<'EOF'
kernel.unprivileged_userns_clone=1
user.max_user_namespaces=15000
kernel.apparmor_restrict_unprivileged_userns=0
EOF

sudo sysctl --system
```

Smoke-test `boxsh` directly as the service user:

```bash
$ANNA_HOME/bin/boxsh --version
$ANNA_HOME/bin/boxsh --rpc --sandbox
```

If the second command exits immediately with a `uid_map` error, the host is still blocking user namespace setup. If you need Anna working before the host is fixed, switch the agent sandbox backend to `local` as a temporary fallback.

### LaunchAgent (macOS)

A ready-to-use plist is provided at [`scripts/com.vaayne.anna.plist`](https://github.com/vaayne/anna/blob/main/scripts/com.vaayne.anna.plist).

```bash
# Install — substitutes $HOME and anna binary path automatically
sed "s|HOME_DIR|$HOME|g; s|ANNA_BIN|$(which anna)|g" scripts/com.vaayne.anna.plist \
  > ~/Library/LaunchAgents/com.vaayne.anna.plist
mkdir -p ~/Library/Logs/anna

launchctl load ~/Library/LaunchAgents/com.vaayne.anna.plist

# Manage
launchctl start com.vaayne.anna
launchctl stop  com.vaayne.anna

# Uninstall
launchctl unload ~/Library/LaunchAgents/com.vaayne.anna.plist
rm ~/Library/LaunchAgents/com.vaayne.anna.plist
```

Logs are written to `~/Library/Logs/anna/anna.log`. The agent starts automatically on login and restarts on crash. Configure API keys and everything else via `anna --open` or the admin panel.

## Docker

Images are published to `ghcr.io/vaayne/anna` for `linux/amd64` and `linux/arm64`.

### Tags

| Tag            | Description           |
| -------------- | --------------------- |
| `latest`       | Latest stable release |
| `v1.2.3`       | Specific version      |
| `sha-<commit>` | Specific commit       |

### Quick Start

First, run setup to configure anna:

```bash
docker run -it --rm \
  --security-opt seccomp=unconfined \
  -v ~/.anna:/home/nonroot/.anna \
  -p 8080:8080 \
  ghcr.io/vaayne/anna:latest \
  anna --open
```

Then start the daemon:

```bash
docker run -d \
  --name anna \
  --security-opt seccomp=unconfined \
  -v ~/.anna:/home/nonroot/.anna \
  -e ANTHROPIC_API_KEY=sk-... \
  ghcr.io/vaayne/anna:latest
```

The container runs as `nonroot` user. Mount `~/.anna` to persist the database, skills, and cache. You can set `ANNA_HOME` to change the data directory inside the container. If you want the default boxsh-backed sandbox to work inside Docker, run the container with `--security-opt seccomp=unconfined` so boxsh can call `unshare(2)`. Without that option, sandboxed core tools fall back to a Docker runtime limitation rather than an anna bug.

### Docker Compose

```yaml
# docker-compose.yml
services:
  anna:
    image: ghcr.io/vaayne/anna:latest
    restart: unless-stopped
    security_opt:
      - seccomp=unconfined
    volumes:
      - ./anna-data:/home/nonroot/.anna
    environment:
      - ANTHROPIC_API_KEY=sk-...
      # - OPENAI_API_KEY=sk-...
```

```bash
docker compose up -d
```

To run initial setup, use `docker compose exec anna anna --open` or start the daemon with `--port 8080` and configure via the web UI.

### Build Locally

```bash
# Single platform
docker build -t anna .

# Multi-platform
docker buildx build --platform linux/amd64,linux/arm64 -t anna .
```

## Volumes & Data

All data lives under the anna home directory (`~/.anna` by default, configurable via `ANNA_HOME`).

| Path                                    | Purpose                                     |
| --------------------------------------- | ------------------------------------------- |
| `~/.anna/anna.db`                       | Single database (config, memory, scheduler) |
| `~/.anna/workspaces/{agent-id}/skills/` | Per-agent installed skills                  |
| `~/.anna/workspaces/{agent-id}/SOUL.md` | Optional per-agent soul/identity override   |
| `~/.anna/cache/`                        | Model cache (regenerable, safe to delete)   |

The `anna.db` file is the only critical data to back up. It contains all configuration, message history, summaries, and scheduler jobs.

## Environment Variables

Configuration is managed through the admin panel (via `anna --open` or `--port`). `HOST` and `PORT` are supported for binding the admin server, and only a small set of other environment variables is supported:

| Variable            | Required | Description                             |
| ------------------- | -------- | --------------------------------------- |
| `ANNA_HOME`         | No       | Anna home directory (default `~/.anna`) |
| `ANTHROPIC_API_KEY` | Yes\*    | Anthropic provider key                  |
| `OPENAI_API_KEY`    | Yes\*    | OpenAI provider key                     |

\* At least one provider key is required. API keys can also be configured via the admin panel.

## Health Check

The daemon logs to stdout. Verify it is running:

```bash
# Binary
anna  # Logs appear in terminal

# Docker
docker logs anna
```
