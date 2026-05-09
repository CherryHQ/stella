---
title: Deployment
---

Two deployment methods: **binary** (direct install) and **Docker**.

## Binary

### From Release

Download a pre-built binary from [GitHub Releases](https://github.com/CherryHQ/stella/releases). Binaries are available for linux, macOS, and Windows on amd64/arm64.

```bash
# Example: Linux amd64
curl -LO https://github.com/CherryHQ/stella/releases/latest/download/stella_linux_amd64.tar.gz
tar xzf stella_linux_amd64.tar.gz
chmod +x stella
sudo mv stella /usr/local/bin/
```

### From Source

```bash
go install github.com/CherryHQ/stella@latest
# or
git clone https://github.com/CherryHQ/stella.git
cd stella && go build -o stella .
```

### Running

Run the setup command to open the admin panel and configure stella (providers, channels, agents, etc.):

```bash
stella --open
```

This starts a local web UI where you set up API keys, channels, and agent profiles. All configuration is stored in `~/.stella/stella.db` -- no manual config files needed.

Start the daemon:

```bash
stella
```

To serve the admin panel alongside the daemon (for runtime config changes):

```bash
stella --port 8080
stella --host 0.0.0.0 --port 8080
```

### Version And Self-Upgrade

```bash
stella version
stella upgrade
stella upgrade --install-dir "$HOME/.local/bin"
```

`stella upgrade` fetches the latest stable release from GitHub, downloads the matching archive for the current OS/architecture, and installs the binary into `$HOME/.local/bin` by default.

### Systemd Service (Linux)

A ready-to-use unit file is provided at [`scripts/stella.service`](https://github.com/CherryHQ/stella/blob/main/scripts/stella.service).

```bash
# Create a dedicated user
sudo useradd --system --no-create-home --shell /bin/false stella
sudo mkdir -p /home/stella/.stella
sudo chown stella:stella /home/stella/.stella

# Install the unit file, substituting the actual stella binary path
sudo sed "s|STELLA_BIN|$(which stella)|g" scripts/stella.service \
  > /etc/systemd/system/stella.service
sudo systemctl daemon-reload
sudo systemctl enable --now stella
sudo journalctl -u stella -f   # follow logs
```

All configuration (channels, agents, scheduler jobs) is stored in `stella.db`. Use `stella --open` or the admin panel to manage it.

### boxsh Sandbox Prerequisites (Linux)

On Linux, Stella uses the managed `boxsh` sandbox by default for the local workspace tools (`bash`, `read`, `write`, `edit`). `boxsh` needs user namespaces and subordinate ID mapping support on the host.

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
grep '^stella:' /etc/subuid
grep '^stella:' /etc/subgid
```

Expected shape:

```text
stella:100000:65536
```

If the entries are missing, add them:

```bash
sudo usermod --add-subuids 100000-165535 stella
sudo usermod --add-subgids 100000-165535 stella
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
sudo tee /etc/sysctl.d/99-stella-boxsh.conf >/dev/null <<'EOF'
kernel.unprivileged_userns_clone=1
user.max_user_namespaces=15000
kernel.apparmor_restrict_unprivileged_userns=0
EOF

sudo sysctl --system
```

Smoke-test `boxsh` directly as the service user:

```bash
$STELLA_HOME/bin/boxsh --version
$STELLA_HOME/bin/boxsh --rpc --sandbox
```

If the second command exits immediately with a `uid_map` error, the host is still blocking user namespace setup. If you need Stella working before the host is fixed, configure the agent sandbox backend to `docker` as a temporary fallback (requires a reachable docker daemon).

### LaunchAgent (macOS)

A ready-to-use plist is provided at [`scripts/com.vaayne.stella.plist`](https://github.com/CherryHQ/stella/blob/main/scripts/com.vaayne.stella.plist).

```bash
# Install — substitutes $HOME and stella binary path automatically
sed "s|HOME_DIR|$HOME|g; s|STELLA_BIN|$(which stella)|g" scripts/com.vaayne.stella.plist \
  > ~/Library/LaunchAgents/com.vaayne.stella.plist
mkdir -p ~/Library/Logs/stella

launchctl load ~/Library/LaunchAgents/com.vaayne.stella.plist

# Manage
launchctl start com.vaayne.stella
launchctl stop  com.vaayne.stella

# Uninstall
launchctl unload ~/Library/LaunchAgents/com.vaayne.stella.plist
rm ~/Library/LaunchAgents/com.vaayne.stella.plist
```

Logs are written to `~/Library/Logs/stella/stella.log`. The agent starts automatically on login and restarts on crash. Configure API keys and everything else via `stella --open` or the admin panel.

## Docker

Images are published to `ghcr.io/cherryhq/stella` for `linux/amd64` and `linux/arm64`.

### Tags

| Tag            | Description           |
| -------------- | --------------------- |
| `latest`       | Latest stable release |
| `v1.2.3`       | Specific version      |
| `sha-<commit>` | Specific commit       |

### Quick Start

First, run setup to configure stella:

```bash
docker run -it --rm \
  --security-opt seccomp=unconfined \
  -v ~/.stella:/home/nonroot/.stella \
  -p 8080:8080 \
  ghcr.io/cherryhq/stella:latest \
  stella --open
```

Then start the daemon:

```bash
docker run -d \
  --name stella \
  --security-opt seccomp=unconfined \
  -v ~/.stella:/home/nonroot/.stella \
  -e ANTHROPIC_API_KEY=sk-... \
  ghcr.io/cherryhq/stella:latest
```

The container runs as `nonroot` user. Mount `~/.stella` to persist the database, skills, and cache. You can set `STELLA_HOME` to change the data directory inside the container. If you want the default boxsh-backed sandbox to work inside Docker, run the container with `--security-opt seccomp=unconfined` so boxsh can call `unshare(2)`. Without that option, sandboxed core tools fall back to a Docker runtime limitation rather than an stella bug.

### Docker Compose

```yaml
# docker-compose.yml
services:
  stella:
    image: ghcr.io/cherryhq/stella:latest
    restart: unless-stopped
    security_opt:
      - seccomp=unconfined
    volumes:
      - ./stella-data:/home/nonroot/.stella
    environment:
      - ANTHROPIC_API_KEY=sk-...
      # - OPENAI_API_KEY=sk-...
```

```bash
docker compose up -d
```

To run initial setup, use `docker compose exec stella stella --open` or start the daemon with `--port 8080` and configure via the web UI.

### Build Locally

```bash
# Single platform
docker build -t stella .

# Multi-platform
docker buildx build --platform linux/amd64,linux/arm64 -t stella .
```

## Docker as a Sandbox Backend

Running stella inside a Docker container (described above) is separate from using Docker as a sandbox backend for agent tool execution. The two can be combined (Docker-in-Docker or a mounted socket), but each is independently useful.

### When to prefer the `docker` sandbox backend

- **Windows**: `boxsh` is Linux/macOS only. The `docker` backend gives Windows users a real isolation boundary via Docker Desktop.
- **Custom toolchain**: You need a specific Python/Node/Go version or a clean Linux userspace that differs from the host.
- **Side-effect isolation**: You want reproducible filesystem state and do not want host-level side effects from agent scripts.

### Tradeoffs

- **Startup latency**: ~200ms for a warm container start; ~1–3s on first pull.
- **Bind-mount performance**: On Docker Desktop for macOS/Windows, bind-mount filesystem operations are 5–20× slower than native disk. Avoid the `docker` backend for heavy read/write workloads on those platforms.
- **No copy-on-write isolation**: Unlike `boxsh` (which uses overlayfs), the docker backend does not provide overlay-based COW. A runaway script can modify or damage the mounted workspace.

See the [Configuration guide](/docs/getting-started/configuration) for `sandbox.docker` config keys and an example JSON payload.

## Volumes & Data

All data lives under the stella home directory (`~/.stella` by default, configurable via `STELLA_HOME`).

| Path                                      | Purpose                                     |
| ----------------------------------------- | ------------------------------------------- |
| `~/.stella/stella.db`                     | Single database (config, memory, scheduler) |
| `~/.stella/workspaces/{agent-id}/skills/` | Per-agent installed skills                  |
| `~/.stella/workspaces/{agent-id}/SOUL.md` | Optional per-agent soul/identity override   |
| `~/.stella/cache/`                        | Model cache (regenerable, safe to delete)   |

The `stella.db` file is the only critical data to back up. It contains all configuration, message history, summaries, and scheduler jobs.

## Environment Variables

Configuration is managed through the admin panel (via `stella --open` or `--port`). `HOST` and `PORT` are supported for binding the admin server, and only a small set of other environment variables is supported:

| Variable            | Required | Description                                 |
| ------------------- | -------- | ------------------------------------------- |
| `STELLA_HOME`       | No       | Stella home directory (default `~/.stella`) |
| `ANTHROPIC_API_KEY` | Yes\*    | Anthropic provider key                      |
| `OPENAI_API_KEY`    | Yes\*    | OpenAI provider key                         |

\* At least one provider key is required. API keys can also be configured via the admin panel.

## Health Check

The daemon logs to stdout. Verify it is running:

```bash
# Binary
stella  # Logs appear in terminal

# Docker
docker logs stella
```
