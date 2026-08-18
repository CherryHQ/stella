---
title: Deployment
---

## Install

### Homebrew (macOS and Linux)

```bash
brew install CherryHQ/tap/stella
```

If you do not set `STELLA_DATABASE_URL`, run `stellad postgres download` once before starting the service.

### Linux packages (.deb / .rpm)

Pre-built packages are available on the [Releases](https://github.com/CherryHQ/stella/releases) page. `bubblewrap` is declared as a dependency and will be installed automatically.

```bash
# Debian / Ubuntu
sudo apt install ./stella_*_linux_amd64.deb

# Fedora / RHEL
sudo dnf install ./stella_*_linux_amd64.rpm
```

If you do not set `STELLA_DATABASE_URL`, run `stellad postgres download` once before starting the service.

### Binary

Download a pre-built binary from [GitHub Releases](https://github.com/CherryHQ/stella/releases) for Linux or macOS (amd64/arm64), then place it on your `$PATH`.

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

Native Stella servers are supported on Linux and macOS. Durable Home and mutable Skill authority require POSIX `openat`, atomic no-replace publication, and filesystem durability semantics; releases therefore do not publish Windows server binaries. A source-built Windows binary rejects `server` and `upgrade` before configuration, database startup, or storage mutation. Move an existing Windows deployment's database and complete `STELLA_HOME` to durable POSIX storage on Linux or macOS before upgrading. Running Stella inside a Linux VM or container on a Windows machine is supported only when `STELLA_HOME` is backed by storage with those POSIX semantics, not a Windows filesystem bind mount.

Stella bundles the Xberg document runtime on Linux and macOS, so supported Library document uploads need no separate system package or startup download.

Start the server — the Web UI is available at `http://localhost:25678`:

```bash
stellad server
```

This starts the server and the Web UI where you configure API keys, channels, and agent profiles. All configuration is stored in PostgreSQL — either an embedded cluster under `~/.stella`, or an external server when you set `STELLA_DATABASE_URL`. If the embedded runtime is missing, run `stellad postgres download` once before `stellad server`. No config files needed.

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

### Structured Reflect and Curator

Structured Reflect is always enabled, and the lifecycle curator defaults to `armed`.

After deployment:

1. Confirm a complete Reflect scheduler run advances the independent Fact and Skill watermarks and reports expected model calls, writes, no-ops, latency, and errors.
2. Confirm the curator runs in `armed` and changes only eligible Reflect-owned Knowledge and Skills.
3. Verify eligible Knowledge can be recovered. Reflect-owned Skills deleted by the curator are not recoverable.
4. To stop future lifecycle writes, set `STELLA_REFLECT_CURATOR_MODE=shadow` and restart. Shadow continues deterministic scans and telemetry without deprecating Knowledge or deleting Skills.

If the entire release must be rolled back, deploy the previous binary rather than selecting a legacy writer in the new release. The cutover migration preserves old global watermark rows and initializes both structured line watermarks so the previous release can resume conservatively.

See [Configuration](/docs/start-here/configuration#environment-variables) for accepted values. The [memory internals page](/docs/development/memory-internals#structured-reflect-and-curator) explains the watermark migration, Shadow behavior, and fail-closed wiring.

## Install the Web UI as an App

The Web UI is a progressive web app, so you can install it to your dock, taskbar,
or home screen and open it in its own window without browser chrome.

- **Chrome, Edge, Brave (desktop and Android)** — open the Web UI and choose the
  install icon in the address bar, or **Menu → Install Stella**.
- **Safari on iPhone and iPad** — open the Web UI and choose **Share → Add to Home Screen**.
- **Safari on macOS** — choose **File → Add to Dock**.

After you have opened the installed app a couple of times, it shows the interface
instead of a browser error page when you launch it offline. Talking to your agents
still needs the server, so anything beyond the interface itself waits for the
connection to return.

When you upgrade Stella, any app or tab left open offers to reload so it picks up
the new version. You can dismiss the prompt and keep working; it comes back the
next time you upgrade.

Browsers only allow installation on a secure origin. If you reach Stella over
plain `http://` on a LAN address, no install option appears and the app stays a
normal browser tab. Put Stella behind HTTPS — see
[the three URL roles](#the-three-url-roles) — or open it at `http://localhost`,
which browsers treat as secure.

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
  ghcr.io/cherryhq/stella:latest \
  stellad server
```

The container runs as `nonroot` user. Mount `$STELLA_HOME` (usually `~/.stella`) to preserve Agent working trees and project Skills, unmirrored assets, and caches; database-backed mutable Skills remain in external PostgreSQL. Release-provided builtins come from the image's immutable bundle, not the host. You can set `STELLA_HOME` to change the data directory inside the container. The `--security-opt seccomp=unconfined` flag is required for the local sandbox backend (bwrap) to call `unshare(2)` inside the container.

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

For Kubernetes, use the production Helm chart and its walkthrough in the [Kubernetes deployment guide](/docs/admin/kubernetes); the rest of this section explains the concepts the chart configures for you.

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

Uploaded user assets need durable POSIX storage under `STELLA_HOME`; S3 configuration does not mirror or recover this mutable tree. Stella currently exposes only the single-replica Helm topology. Future replicas will require one shared, strongly consistent POSIX namespace. `STELLA_BLOB_S3_*` is optional and serves separate immutable BlobStore data such as content-addressed session media.

A loopback base URL is never a startup error — it is legitimate when you reach Stella via `localhost` or `kubectl port-forward` — but Stella logs a loud warning when OAuth/OIDC login is configured against one, because login redirects would point back at the pod. Deployment charts should make `STELLA_BASE_URL` a required value; that layer knows it sits behind an ingress.

### Required environment for a managed deployment

| Variable                     | Value                                                                                    |
| ---------------------------- | ---------------------------------------------------------------------------------------- |
| `STELLA_DATABASE_URL`        | External PostgreSQL DSN with `pgvector` + `pg_search`                                    |
| `STELLA_VAULT_KEY`           | age secret key for the vault (generate with `stellad vault keygen`)                      |
| `STELLA_BASE_URL`            | Public canonical URL clients use (e.g. `https://stella.example.com`)                     |
| `STELLA_REQUIRE_EXTERNAL_DB` | `1` — already set by the Docker image; fail fast instead of starting embedded PostgreSQL |

A full Kubernetes manifest walkthrough is out of scope here.

### Graceful shutdown and probes

Stella exposes two unauthenticated infrastructure endpoints for orchestrators:

| Path       | Meaning                                                                                                                              |
| ---------- | ------------------------------------------------------------------------------------------------------------------------------------ |
| `/healthz` | Liveness. `200` whenever the process is running and can serve HTTP. Never depends on the database or drain state.                    |
| `/readyz`  | Readiness. `200` only when startup is complete, shutdown has not begun, and a `2s` database ping succeeds; else `503` with a reason. |

On the first `SIGTERM`/`SIGINT` the daemon runs a **two-phase graceful drain** rather than stopping immediately:

1. `/readyz` flips to `503` and SSE observers detach, so load balancers stop routing new traffic. In-flight turns continue as server-owned accepted work and persist before shutdown completes.
2. In-flight HTTP requests drain within `STELLA_HTTP_SHUTDOWN_TIMEOUT`; anything still open when the budget is spent is force-closed.
3. Background jobs (goal and scheduler agent runs) keep executing and drain within `STELLA_RIVER_SOFT_STOP_TIMEOUT`; jobs still running when that budget is spent are cancelled.

A **second** signal during the drain collapses to an immediate hard stop. These two budgets bound the drain; they do **not** guarantee any single long-running LLM turn finishes.

| Variable                         | Default | Purpose                                                                                           |
| -------------------------------- | ------- | ------------------------------------------------------------------------------------------------- |
| `STELLA_HTTP_SHUTDOWN_TIMEOUT`   | `60s`   | Drain budget for in-flight HTTP requests before open connections are force-closed. Must be `> 0`. |
| `STELLA_RIVER_SOFT_STOP_TIMEOUT` | `120s`  | Drain budget for in-flight background jobs before their contexts are cancelled. Must be `> 0`.    |

Both take a Go duration (`60s`, `2m`, `500ms`). An unparseable or non-positive value fails startup fast.

The drain starts as soon as the signal lands. On Kubernetes, use a `preStop` sleep to give endpoint propagation a head start: the kubelet removes the pod from endpoints when termination begins, and the sleep delays `SIGTERM` until routing has caught up.

Example probe and lifecycle configuration:

```yaml
readinessProbe:
  httpGet:
    path: /readyz
    port: 25678
  periodSeconds: 5
  failureThreshold: 3
livenessProbe:
  httpGet:
    path: /healthz
    port: 25678
  periodSeconds: 10
  failureThreshold: 3
lifecycle:
  preStop:
    sleep:
      seconds: 5
# Must exceed the full drain so the kubelet does not SIGKILL mid-drain:
#   terminationGracePeriodSeconds >=
#     preStop sleep                  (endpoint propagation)
#   + STELLA_HTTP_SHUTDOWN_TIMEOUT   (HTTP drain budget)
#   + STELLA_RIVER_SOFT_STOP_TIMEOUT (background-job drain budget)
#   + cleanup margin
# At the defaults (5 + 60 + 120 + margin) use >= 200.
terminationGracePeriodSeconds: 200
```

## Sandbox Backends

Running Stella inside a Docker container (described above) is separate from using Docker as a sandbox backend for agent tool execution. Stella supports three sandbox backends: `docker`, `local`, and `none`. See the [Sandbox guide](/docs/guides/sandbox) for how to choose a backend, configure Docker sandbox modes, and troubleshoot common issues.

## Volumes & Data

All data lives under the stella home directory (`~/.stella` by default, configurable via `STELLA_HOME`).

| Path                                          | Purpose                                                                                       |
| --------------------------------------------- | --------------------------------------------------------------------------------------------- |
| `~/.stella/postgres/`                         | Embedded PostgreSQL data (config, memory, scheduler); absent when using `STELLA_DATABASE_URL` |
| `~/.stella/pg-runtime/`                       | Downloaded embedded PostgreSQL runtime; recreate with `stellad postgres download`             |
| `~/.stella/bundles/{revision}/`               | Exact release-provided builtin Skill bundle; derived from the matching binary                 |
| `~/.stella/agents/{agent-id}/.agents/skills/` | Derived `system_agent` Skill execution cache                                                  |
| `~/.stella/agents/{agent-id}/SOUL.md`         | Optional per-agent soul/identity override                                                     |
| `~/.stella/cache/sandbox-tmp/`                | Docker sandbox temporary directories; scratch, stale directories removed at startup           |

Preserve PostgreSQL, durable Agent/project working trees including project Skills, and unmirrored asset trees. PostgreSQL contains configuration, message history, summaries, scheduler jobs, and mutable `system`, `system_agent`, `user`, and `user_agent` Skills. With the embedded cluster, back up `~/.stella/postgres/` with the server stopped; `~/.stella/pg-runtime/`, `~/.stella/bundles/{revision}/`, and Skill execution caches are derived and can be recreated. With an external server, use `pg_dump` against your `STELLA_DATABASE_URL` database.

For a full breakdown of which directories are durable data, derived cache, or scratch — and the volume and backup treatment each needs on Kubernetes or ephemeral disks — see [Storage & Durability](/docs/start-here/storage).

## Environment Variables

Configuration is managed through the Web UI (default `http://localhost:25678`; use `--port` to change). `HOST` and `PORT` are supported for binding the server, and only a small set of other environment variables is supported:

| Variable                         | Required                  | Description                                                                                                                                                                   |
| -------------------------------- | ------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `STELLA_HOME`                    | No                        | Stella home directory (default `~/.stella`)                                                                                                                                   |
| `STELLA_DATABASE_URL`            | Docker: yes; otherwise no | External PostgreSQL connection URL; unset uses the embedded cluster under `STELLA_HOME` outside Docker                                                                        |
| `STELLA_BASE_URL`                | No¶                       | Public canonical URL for OAuth callbacks and channel deep links; unset derives from the bind host (loopback)                                                                  |
| `STELLA_REQUIRE_EXTERNAL_DB`     | No                        | Fail startup when `STELLA_DATABASE_URL` is unset instead of starting embedded PostgreSQL; the Docker image sets `1`, override with `0` for embedded PG on a persistent volume |
| `STELLA_HTTP_SHUTDOWN_TIMEOUT`   | No                        | Graceful-shutdown drain budget for in-flight HTTP requests (Go duration, default `60s`, `> 0`)                                                                                |
| `STELLA_RIVER_SOFT_STOP_TIMEOUT` | No                        | Graceful-shutdown drain budget for in-flight background jobs (Go duration, default `120s`, `> 0`)                                                                             |
| `STELLA_BLOB_S3_ENDPOINT`        | No§                       | S3-compatible endpoint for immutable BlobStore data                                                                                                                           |
| `STELLA_BLOB_S3_BUCKET`          | No§                       | Bucket for immutable BlobStore data                                                                                                                                           |
| `STELLA_BLOB_S3_ACCESS_KEY`      | No§                       | Access key for immutable BlobStore data                                                                                                                                       |
| `STELLA_BLOB_S3_SECRET_KEY`      | No§                       | Secret key for immutable BlobStore data                                                                                                                                       |
| `STELLA_BLOB_S3_REGION`          | No                        | Optional S3 region                                                                                                                                                            |
| `STELLA_BLOB_S3_USE_SSL`         | No                        | Use HTTPS for S3-compatible storage; defaults to `true`                                                                                                                       |
| `STELLA_VAULT_KEY`               | Yes†                      | age secret key for the vault — required for secrets, OAuth, and bearer tokens                                                                                                 |
| `STELLA_DOCKER_SANDBOX_MODE`     | No‡                       | Required only for the `docker` sandbox backend: `host`, `bind`, or `volume`                                                                                                   |
| `STELLA_HOME_HOST`               | No‡                       | Host-side path backing `STELLA_HOME` — required only when `STELLA_DOCKER_SANDBOX_MODE=bind`                                                                                   |
| `STELLA_HOME_VOLUME`             | No‡                       | Docker named volume backing `STELLA_HOME` — required only when `STELLA_DOCKER_SANDBOX_MODE=volume`                                                                            |

† Without `STELLA_VAULT_KEY`, vault endpoints return `503`, OAuth tokens cannot be issued, and plugin secrets are not injected. Generate a key with `age-keygen`.

‡ Required only when agents use the `docker` sandbox backend. Use `host` when stellad runs on the host, `bind` when stellad runs in Docker with a host bind mount, and `volume` when stellad runs in Docker with a named volume.

§ Set all four required S3 variables together, or leave all unset. Partial blob-store configuration fails startup. Mutable assets never require these variables.

¶ Required for managed deployments, and whenever OAuth login or channel deep links are used. See [Managed Deployment](#managed-deployment).

## Health Check

The daemon logs to stdout. Verify it is running:

```bash
# Binary
stellad server  # Logs appear in terminal

# Docker
docker logs stella
```

For an HTTP check, hit the infrastructure probes (no authentication required):

```bash
curl -fsS http://localhost:25678/healthz   # liveness: process is up
curl -fsS http://localhost:25678/readyz    # readiness: up, not draining, DB reachable
```

See [Graceful shutdown and probes](#graceful-shutdown-and-probes) for using these under Kubernetes.
