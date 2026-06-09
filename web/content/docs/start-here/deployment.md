---
title: Deployment
---

## Install

### Homebrew (macOS and Linux)

```bash
brew install CherryHQ/tap/stella
```

### Linux packages (.deb / .rpm)

Pre-built packages are available on the [Releases](https://github.com/CherryHQ/stella/releases) page. `bubblewrap` is declared as a dependency and will be installed automatically.

```bash
# Debian / Ubuntu
sudo apt install ./stella_*_linux_amd64.deb

# Fedora / RHEL
sudo dnf install ./stella_*_linux_amd64.rpm
```

### Binary

Download a pre-built binary from [GitHub Releases](https://github.com/CherryHQ/stella/releases) for linux, macOS, or Windows (amd64/arm64), then place it on your `$PATH`.

```bash
# Example: Linux amd64
curl -LO https://github.com/CherryHQ/stella/releases/latest/download/stella_linux_amd64.tar.gz
tar xzf stella_linux_amd64.tar.gz
chmod +x stella stellad
sudo mv stella stellad /usr/local/bin/
```

### Go

```bash
go install github.com/CherryHQ/stella/cmd/stella@latest
go install github.com/CherryHQ/stella/cmd/stellad@latest
# or
git clone https://github.com/CherryHQ/stella.git
cd stella && go build -o stella ./cmd/stella/ && go build -o stellad ./cmd/stellad/
```

## Run

Start the server — the Web UI is available at `http://localhost:25678`:

```bash
stellad server
```

This starts the server and the Web UI where you configure API keys, channels, and agent profiles. All configuration is stored in `~/.stella/stella.db` — no config files needed.

```bash
stellad server --port 8080             # custom port
stellad server --host 0.0.0.0 --port 8080  # bind to all interfaces
```

### Version and Self-Upgrade

```bash
stella version
stellad upgrade
stellad upgrade --install-dir "$HOME/.local/bin"  # custom install path
```

`stellad upgrade` fetches the latest stable release from GitHub, downloads the matching archive for the current OS/architecture, and replaces the running `stellad` binary by default. If the target directory is not writable, rerun the command with the required OS permission or use `--install-dir`. If the binary is locked or busy, stop the running Stella process or service first, then retry.

## Run as a Background Service

### macOS — Homebrew

```bash
brew services start stella   # start on login, restart on crash
brew services stop stella
brew services restart stella
```

### macOS — manual

```bash
stellad service install       # install LaunchAgent and start
stellad service status
stellad service logs --follow
stellad service stop
stellad service start
stellad service restart
stellad service uninstall
```

Logs are written to `~/Library/Logs/stella/stella.log`. The agent starts automatically on login and restarts on crash.

### Linux — systemd user mode (no root required)

The service runs as your user and starts on login. `bubblewrap` must be installed first (pulled in automatically by Homebrew and package-manager installs; for raw binary installs: `apt install bubblewrap` / `dnf install bubblewrap`).

```bash
stellad service install
stellad service status
stellad service logs --follow
stellad service stop
stellad service start
stellad service restart
stellad service uninstall
```

The unit file is installed to `~/.config/systemd/user/stella.service`.

### Linux — systemd system mode (root required)

Runs as root, starts on boot.

```bash
sudo stellad service install --system
stellad service status
stellad service logs --follow
sudo stellad service uninstall --system
```

The unit file is installed to `/etc/systemd/system/stella.service`.

## Docker

Images are published to `ghcr.io/cherryhq/stella` for `linux/amd64` and `linux/arm64`.

### Tags

| Tag            | Description           |
| -------------- | --------------------- |
| `latest`       | Latest stable release |
| `v1.2.3`       | Specific version      |
| `sha-<commit>` | Specific commit       |

### Quick Start

First, run stella with `--port 8080` to configure it via the Web UI:

```bash
docker run -it --rm \
  --security-opt seccomp=unconfined \
  -v ~/.stella:/home/nonroot/.stella \
  -p 8080:8080 \
  ghcr.io/cherryhq/stella:latest \
  stellad server --port 8080
```

Then start the server:

```bash
docker run -d \
  --name stella \
  --security-opt seccomp=unconfined \
  -v ~/.stella:/home/nonroot/.stella \
  -p 25678:25678 \
  -e ANTHROPIC_API_KEY=sk-... \
  ghcr.io/cherryhq/stella:latest \
  stellad server
```

The container runs as `nonroot` user. Mount `~/.stella` to persist the database, skills, and cache. You can set `STELLA_HOME` to change the data directory inside the container. The `--security-opt seccomp=unconfined` flag is required for the local sandbox backend (bwrap) to call `unshare(2)` inside the container.

### Docker Compose

Choose the compose shape based on where Stella runs and which sandbox backend agents use:

| Stella runtime | Sandbox backend | Data mount                 | Extra setup                                                                                                                                                        |
| -------------- | --------------- | -------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Docker         | `local`         | bind mount or named volume | Add `seccomp=unconfined` so bubblewrap can create namespaces.                                                                                                      |
| Docker         | `none`          | bind mount or named volume | No sandbox-specific Docker option. Use only for trusted workloads.                                                                                                 |
| Docker         | `docker`        | host bind mount            | Mount the Docker socket, set `STELLA_DOCKER_SANDBOX_MODE=bind`, and set `STELLA_HOME_HOST` to the host-side path.                                                  |
| Docker         | `docker`        | Docker named volume        | Mount the Docker socket, set `STELLA_DOCKER_SANDBOX_MODE=volume`, and set `STELLA_HOME_VOLUME` to the volume name. Requires Docker Engine 25+ for volume subpaths. |
| Host           | `docker`        | normal host directory      | Set `STELLA_DOCKER_SANDBOX_MODE=host`; paths are already daemon-visible.                                                                                           |

#### Container with `local` or `none` sandbox

This is the simplest deployment. Keep `seccomp=unconfined` if agents use the `local` sandbox; remove it if you deliberately use `none`.

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

#### Container with `docker` sandbox and a host bind mount

Use this when `STELLA_HOME` is mounted from a host directory. The Docker daemon sees the host path, not `/home/nonroot/.stella` inside the Stella container, so declare `bind` mode and point `STELLA_HOME_HOST` at the same directory from the host's view.

```yaml
# docker-compose.yml
services:
  stella:
    image: ghcr.io/cherryhq/stella:latest
    restart: unless-stopped
    volumes:
      - ./stella-data:/home/nonroot/.stella
      - /var/run/docker.sock:/var/run/docker.sock
    environment:
      - ANTHROPIC_API_KEY=sk-...
      - STELLA_DOCKER_SANDBOX_MODE=bind
      - STELLA_HOME_HOST=${PWD}/stella-data
```

#### Container with `docker` sandbox and a named volume

Use this when `STELLA_HOME` is a Docker named volume. Declare `volume` mode; Stella then uses volume subpath mounts so sandbox containers can access workspaces and tool directories without a host-visible bind-mount path.

```yaml
# docker-compose.yml
services:
  stella:
    image: ghcr.io/cherryhq/stella:latest
    restart: unless-stopped
    volumes:
      - stella-data:/home/nonroot/.stella
      - /var/run/docker.sock:/var/run/docker.sock
    environment:
      - ANTHROPIC_API_KEY=sk-...
      - STELLA_DOCKER_SANDBOX_MODE=volume
      - STELLA_HOME_VOLUME=stella-data

volumes:
  stella-data:
```

When agents use the `docker` sandbox, always set `STELLA_DOCKER_SANDBOX_MODE` explicitly: `host`, `bind`, or `volume`. In `bind` mode set only `STELLA_HOME_HOST`; in `volume` mode set only `STELLA_HOME_VOLUME`; in `host` mode set neither. If agents use `local` or `none`, none of these Docker-sandbox variables are needed.

```bash
docker compose up -d
```

To run initial setup, start with `--port 8080` and configure via the Web UI at `http://localhost:8080`, or use `docker compose exec stella stellad server --port 8080`.

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

- **Windows**: The local sandbox backend is Linux/macOS only. The `docker` backend gives Windows users a real isolation boundary via Docker Desktop.
- **Custom toolchain**: You need a specific Python/Node/Go version or a clean Linux userspace that differs from the host.
- **Side-effect isolation**: You want reproducible filesystem state and do not want host-level side effects from agent scripts.

### Tradeoffs

- **Startup latency**: ~200ms for a warm container start; ~1–3s on first pull.
- **Bind-mount performance**: On Docker Desktop for macOS/Windows, bind-mount filesystem operations are 5–20× slower than native disk. Avoid the `docker` backend for heavy read/write workloads on those platforms.
- **No copy-on-write isolation**: Unlike the local backend (which uses overlayfs), the docker backend does not provide overlay-based COW. A runaway script can modify or damage the mounted workspace.

See the [Configuration guide](/docs/start-here/configuration) for supported environment variables.

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

Configuration is managed through the Web UI (default `http://localhost:25678`; use `--port` to change). `HOST` and `PORT` are supported for binding the server, and only a small set of other environment variables is supported:

| Variable                     | Required | Description                                                                                        |
| ---------------------------- | -------- | -------------------------------------------------------------------------------------------------- |
| `STELLA_HOME`                | No       | Stella home directory (default `~/.stella`)                                                        |
| `ANTHROPIC_API_KEY`          | Yes\*    | Anthropic provider key                                                                             |
| `OPENAI_API_KEY`             | Yes\*    | OpenAI provider key                                                                                |
| `STELLA_VAULT_KEY`           | Yes†     | age secret key for the vault — required for secrets, OAuth, and bearer tokens                      |
| `STELLA_DOCKER_SANDBOX_MODE` | No‡      | Required only for the `docker` sandbox backend: `host`, `bind`, or `volume`                        |
| `STELLA_HOME_HOST`           | No‡      | Host-side path backing `STELLA_HOME` — required only when `STELLA_DOCKER_SANDBOX_MODE=bind`        |
| `STELLA_HOME_VOLUME`         | No‡      | Docker named volume backing `STELLA_HOME` — required only when `STELLA_DOCKER_SANDBOX_MODE=volume` |

\* At least one provider key is required. API keys can also be configured via the Web UI.

† Without `STELLA_VAULT_KEY`, vault endpoints return `503`, OAuth tokens cannot be issued, and plugin secrets are not injected. Generate a key with `age-keygen`.

‡ Required only when agents use the `docker` sandbox backend. Use `host` when stellad runs on the host, `bind` when stellad runs in Docker with a host bind mount, and `volume` when stellad runs in Docker with a named volume.

## Health Check

The daemon logs to stdout. Verify it is running:

```bash
# Binary
stella  # Logs appear in terminal

# Docker
docker logs stella
```
