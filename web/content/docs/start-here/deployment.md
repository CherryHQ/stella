---
title: Deployment
---

## Install

### Homebrew (macOS and Linux)

```bash
brew install CherryHQ/tap/stella
```

If you do not set `STELLA_DATABASE_URL`, run `stellad postgres download-runtime` once before starting the service.

### Linux packages (.deb / .rpm)

Pre-built packages are available on the [Releases](https://github.com/CherryHQ/stella/releases) page. `bubblewrap` is declared as a dependency and will be installed automatically.

```bash
# Debian / Ubuntu
sudo apt install ./stella_*_linux_amd64.deb

# Fedora / RHEL
sudo dnf install ./stella_*_linux_amd64.rpm
```

If you do not set `STELLA_DATABASE_URL`, run `stellad postgres download-runtime` once before starting the service.

### Binary

Download a pre-built binary from [GitHub Releases](https://github.com/CherryHQ/stella/releases) for linux, macOS, or Windows (amd64/arm64), then place it on your `$PATH`.

```bash
# Example: Linux amd64
curl -LO https://github.com/CherryHQ/stella/releases/latest/download/stella_linux_amd64.tar.gz
tar xzf stella_linux_amd64.tar.gz
chmod +x stellad
sudo mv stellad /usr/local/bin/
```

### Go

```bash
go install github.com/CherryHQ/stella/cmd/stellad@latest
# or
git clone https://github.com/CherryHQ/stella.git
cd stella && go build -o dist/bin/stellad ./cmd/stellad/
```

## Run

Start the server — the Web UI is available at `http://localhost:25678`:

```bash
stellad server
```

This starts the server and the Web UI where you configure API keys, channels, and agent profiles. All configuration is stored in PostgreSQL — either an embedded cluster under `~/.stella`, or an external server when you set `STELLA_DATABASE_URL`. If the embedded runtime is missing, run `stellad postgres download-runtime` once before `stellad server`. No config files needed.

```bash
stellad server --port 8080             # custom port
stellad server --host 0.0.0.0 --port 8080  # bind to all interfaces
```

### Version and Self-Upgrade

```bash
stellad version
stellad upgrade
stellad upgrade 0.50.0                             # install a specific release
stellad upgrade --install-dir "$HOME/.local/bin"  # custom install path
```

`stellad upgrade` fetches a stable release from GitHub (the latest by default, or the version you pass as an argument), downloads the matching archive for the current OS/architecture while showing download progress, and replaces the running `stellad` binary by default. If the target directory is not writable, rerun the command with the required OS permission or use `--install-dir`. If the binary is locked or busy, stop the running Stella process or service first, then retry.

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

Docker images require an external PostgreSQL 18 server with `pg_search` and `pgvector` installed. Set `STELLA_DATABASE_URL`; the embedded runtime download path is for non-Docker installs.

First, run `stellad server` with `--port 8080` to configure it via the Web UI:

```bash
docker run -it --rm \
  --security-opt seccomp=unconfined \
  -v ~/.stella:/home/stella/.stella \
  -p 8080:8080 \
  -e STELLA_DATABASE_URL='postgres://user:pass@postgres.example.com:5432/stella?sslmode=require' \
  ghcr.io/cherryhq/stella:latest \
  stellad server --port 8080
```

Then start the server:

```bash
docker run -d \
  --name stella \
  --security-opt seccomp=unconfined \
  -v ~/.stella:/home/stella/.stella \
  -p 25678:25678 \
  -e STELLA_DATABASE_URL='postgres://user:pass@postgres.example.com:5432/stella?sslmode=require' \
  -e ANTHROPIC_API_KEY=sk-... \
  ghcr.io/cherryhq/stella:latest \
  stellad server
```

The container runs as `nonroot` user. Mount `~/.stella` to persist skills and cache; PostgreSQL data lives in the external database. You can set `STELLA_HOME` to change the data directory inside the container. The `--security-opt seccomp=unconfined` flag is required for the local sandbox backend (bwrap) to call `unshare(2)` inside the container.

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
      - ./stella-data:/home/stella/.stella
    environment:
      - STELLA_DATABASE_URL=postgres://user:pass@postgres.example.com:5432/stella?sslmode=require
      - ANTHROPIC_API_KEY=sk-...
      # - OPENAI_API_KEY=sk-...
```

The `seccomp=unconfined` flag is needed for the `local` sandbox backend (bubblewrap). If agents use the `docker` sandbox backend, you need additional Docker socket mounts and mode-specific environment variables — see the [Sandbox guide](/docs/guides/sandbox#docker-compose-examples) for all compose variants.

```bash
docker compose up -d
```

To run initial setup, start with `--port 8080` and configure via the Web UI at `http://localhost:8080`, or use `docker compose exec stella stellad server --port 8080`. The compose service must have `STELLA_DATABASE_URL` set.

### Build Locally

```bash
# Single platform
docker build -t stella .

# Multi-platform
docker buildx build --platform linux/amd64,linux/arm64 -t stella .
```

## Managed Deployment

When you run Stella under an orchestrator (Kubernetes and similar), two things that are convenient locally become traps: the embedded single-node database and a base URL that points back at the pod. The Docker image refuses the first by default (`STELLA_REQUIRE_EXTERNAL_DB=1`), and Stella warns loudly about the second.

### The three URL roles

Stella uses three distinct addresses. Keep them separate:

| Variable            | Role                                                                                                                                                                     |
| ------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `HOST`              | The interface the server binds to. Use `0.0.0.0` in a container so the pod is reachable; defaults to `127.0.0.1`.                                                        |
| `STELLA_SERVER_URL` | The address the CLI and in-process callers use to reach the local server. Pod-local; defaults to `http://127.0.0.1:25678`.                                               |
| `STELLA_BASE_URL`   | The public, canonical URL clients see. It is the source for OAuth callback URLs and channel deep links, so it must be the externally reachable address — not a loopback. |

Binding to `0.0.0.0` (`HOST`) does **not** give you a public URL: with `STELLA_BASE_URL` unset, the base URL is derived from the bind host and still resolves to loopback. Always set `STELLA_BASE_URL` explicitly in a managed deployment.

### External database requirement

The Docker image sets `STELLA_REQUIRE_EXTERNAL_DB=1`: startup fails with an actionable error when `STELLA_DATABASE_URL` is unset, instead of silently starting the embedded PostgreSQL cluster on the container's ephemeral filesystem — with multiple replicas, each pod would even create its own database. Point `STELLA_DATABASE_URL` at an external PostgreSQL with `pgvector` and `pg_search`. To deliberately run embedded PostgreSQL in a single container backed by a persistent volume, set `STELLA_REQUIRE_EXTERNAL_DB=0`.

A loopback base URL is never a startup error — it is legitimate when you reach Stella via `localhost` or `kubectl port-forward` — but Stella logs a loud warning when OAuth/OIDC login is configured against one, because login redirects would point back at the pod. Deployment charts should make `STELLA_BASE_URL` a required value; that layer knows it sits behind an ingress.

### Required environment for a managed deployment

| Variable                     | Value                                                                                    |
| ---------------------------- | ---------------------------------------------------------------------------------------- |
| `STELLA_DATABASE_URL`        | External PostgreSQL DSN with `pgvector` + `pg_search`                                    |
| `STELLA_VAULT_KEY`           | age secret key for the vault (generate with `stellad vault keygen`)                      |
| `STELLA_BASE_URL`            | Public canonical URL clients use (e.g. `https://stella.example.com`)                     |
| `STELLA_REQUIRE_EXTERNAL_DB` | `1` — already set by the Docker image; fail fast instead of starting embedded PostgreSQL |

A full Kubernetes manifest walkthrough is out of scope here.

## Sandbox Backends

Running Stella inside a Docker container (described above) is separate from using Docker as a sandbox backend for agent tool execution. Stella supports three sandbox backends: `docker`, `local`, and `none`. See the [Sandbox guide](/docs/guides/sandbox) for how to choose a backend, configure Docker sandbox modes, and troubleshoot common issues.

## Volumes & Data

All data lives under the stella home directory (`~/.stella` by default, configurable via `STELLA_HOME`).

| Path                                  | Purpose                                                                                       |
| ------------------------------------- | --------------------------------------------------------------------------------------------- |
| `~/.stella/postgres/`                 | Embedded PostgreSQL data (config, memory, scheduler); absent when using `STELLA_DATABASE_URL` |
| `~/.stella/pg-runtime/`               | Downloaded embedded PostgreSQL runtime; recreate with `stellad postgres download-runtime`     |
| `~/.stella/agents/{agent-id}/skills/` | Per-agent installed skills                                                                    |
| `~/.stella/agents/{agent-id}/SOUL.md` | Optional per-agent soul/identity override                                                     |
| `~/.stella/cache/`                    | Model cache (regenerable, safe to delete)                                                     |

The PostgreSQL data is the only critical data to back up. It contains all configuration, message history, summaries, and scheduler jobs. With the embedded cluster, back up the `~/.stella/postgres/` directory (with the server stopped); `~/.stella/pg-runtime/` is downloaded code and can be recreated. With an external server, use `pg_dump` against your `STELLA_DATABASE_URL` database.

For a full breakdown of which directories are durable data, derived cache, or scratch — and the volume and backup treatment each needs on Kubernetes or ephemeral disks — see [Storage & Durability](/docs/start-here/storage).

## Environment Variables

Configuration is managed through the Web UI (default `http://localhost:25678`; use `--port` to change). `HOST` and `PORT` are supported for binding the server, and only a small set of other environment variables is supported:

| Variable                     | Required                  | Description                                                                                                                                                                   |
| ---------------------------- | ------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `STELLA_HOME`                | No                        | Stella home directory (default `~/.stella`)                                                                                                                                   |
| `STELLA_DATABASE_URL`        | Docker: yes; otherwise no | External PostgreSQL connection URL; unset uses the embedded cluster under `STELLA_HOME` outside Docker                                                                        |
| `STELLA_BASE_URL`            | No¶                       | Public canonical URL for OAuth callbacks and channel deep links; unset derives from the bind host (loopback)                                                                  |
| `STELLA_REQUIRE_EXTERNAL_DB` | No                        | Fail startup when `STELLA_DATABASE_URL` is unset instead of starting embedded PostgreSQL; the Docker image sets `1`, override with `0` for embedded PG on a persistent volume |
| `STELLA_BLOB_S3_ENDPOINT`    | No§                       | S3-compatible endpoint for the durable user-asset mirror                                                                                                                      |
| `STELLA_BLOB_S3_BUCKET`      | No§                       | Bucket for mirrored user-uploaded assets                                                                                                                                      |
| `STELLA_BLOB_S3_ACCESS_KEY`  | No§                       | Access key for the asset mirror                                                                                                                                               |
| `STELLA_BLOB_S3_SECRET_KEY`  | No§                       | Secret key for the asset mirror                                                                                                                                               |
| `STELLA_BLOB_S3_REGION`      | No                        | Optional S3 region                                                                                                                                                            |
| `STELLA_BLOB_S3_USE_SSL`     | No                        | Use HTTPS for S3-compatible storage; defaults to `true`                                                                                                                       |
| `ANTHROPIC_API_KEY`          | Yes\*                     | Anthropic provider key                                                                                                                                                        |
| `OPENAI_API_KEY`             | Yes\*                     | OpenAI provider key                                                                                                                                                           |
| `STELLA_VAULT_KEY`           | Yes†                      | age secret key for the vault — required for secrets, OAuth, and bearer tokens                                                                                                 |
| `STELLA_DOCKER_SANDBOX_MODE` | No‡                       | Required only for the `docker` sandbox backend: `host`, `bind`, or `volume`                                                                                                   |
| `STELLA_HOME_HOST`           | No‡                       | Host-side path backing `STELLA_HOME` — required only when `STELLA_DOCKER_SANDBOX_MODE=bind`                                                                                   |
| `STELLA_HOME_VOLUME`         | No‡                       | Docker named volume backing `STELLA_HOME` — required only when `STELLA_DOCKER_SANDBOX_MODE=volume`                                                                            |

\* At least one provider key is required. API keys can also be configured via the Web UI.

† Without `STELLA_VAULT_KEY`, vault endpoints return `503`, OAuth tokens cannot be issued, and plugin secrets are not injected. Generate a key with `age-keygen`.

‡ Required only when agents use the `docker` sandbox backend. Use `host` when stellad runs on the host, `bind` when stellad runs in Docker with a host bind mount, and `volume` when stellad runs in Docker with a named volume.

§ Set all four required S3 mirror variables together, or leave all unset. Partial blob-store configuration fails startup.

¶ Required for managed deployments, and whenever OAuth login or channel deep links are used. See [Managed Deployment](#managed-deployment).

## Health Check

The daemon logs to stdout. Verify it is running:

```bash
# Binary
stellad server  # Logs appear in terminal

# Docker
docker logs stella
```
