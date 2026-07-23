---
title: Sandbox
---

Stella runs agent code inside a sandbox. You choose the sandbox backend per agent — each backend offers different isolation levels, platform support, and tradeoffs.

## Choosing a Backend

| Scenario                                   | Recommended | Why                                                             |
| ------------------------------------------ | ----------- | --------------------------------------------------------------- |
| Production / multi-user                    | `docker`    | Full container-level process, filesystem, and network isolation |
| Linux without Docker                       | `local`     | OS-level isolation via bubblewrap                               |
| macOS without Docker                       | `none`      | `local` provides no extra isolation on macOS                    |
| Windows                                    | `docker`    | `local` is not supported on Windows                             |
| Trusted single-user local dev              | `none`      | Zero dependencies, no isolation                                 |
| Custom toolchain (specific Python/Node/Go) | `docker`    | Clean Linux userspace independent of host                       |

Change the active sandbox backend from the **Plugins** page in the Web UI. Only one backend is active at a time.

## Docker Backend

Docker provides full container-level process, filesystem, and network isolation. The Docker daemon must be running and reachable. All platforms (Linux, macOS, Windows) are supported.

### When to Use

- You need strong isolation between the agent and the host.
- You need a reproducible Linux environment with a specific toolchain.
- You are running on Windows (the only sandbox option).
- You want side-effect isolation — agent scripts cannot modify the host filesystem outside the mounted workspace.

### Tradeoffs

- **Startup latency**: ~200ms for a warm container start; ~1–3s on first pull.
- **Bind-mount performance**: On Docker Desktop for macOS/Windows, bind-mount filesystem operations are 5–20× slower than native disk. Avoid for heavy read/write workloads on those platforms.
- **No copy-on-write isolation**: Unlike the local backend (which uses overlayfs on Linux), the Docker backend does not provide overlay-based COW. A runaway script can modify or damage the mounted workspace.

### Runtime Modes

When stellad itself runs inside a Docker container and agents use the `docker` sandbox backend, you must tell Stella how the Docker daemon can see `STELLA_HOME`. Set `STELLA_DOCKER_SANDBOX_MODE` to one of:

| Mode     | When to use                                             | Required env                                        |
| -------- | ------------------------------------------------------- | --------------------------------------------------- |
| `host`   | stellad runs on the host (not in a container)           | Neither `STELLA_HOME_HOST` nor `STELLA_HOME_VOLUME` |
| `bind`   | stellad runs in Docker; `STELLA_HOME` is a bind mount   | `STELLA_HOME_HOST` = the host-side path             |
| `volume` | stellad runs in Docker; `STELLA_HOME` is a named volume | `STELLA_HOME_VOLUME` = the volume name              |

Each mode rejects env vars that belong to other modes. For example, `bind` mode with `STELLA_HOME_VOLUME` set is an error.

Volume mode requires Docker Engine 25+ for volume subpath mounts.

### Docker Compose Examples

**Container with `local` or `none` sandbox** — the simplest deployment:

```yaml
services:
  stella:
    image: ghcr.io/cherryhq/stella:latest
    restart: unless-stopped
    security_opt:
      - seccomp=unconfined
    volumes:
      - ./stella-data:/home/stella/.stella
```

Keep `seccomp=unconfined` if agents use the `local` sandbox (bubblewrap needs it); remove it if you use `none`.

**Container with `docker` sandbox and a host bind mount:**

```yaml
services:
  stella:
    image: ghcr.io/cherryhq/stella:latest
    restart: unless-stopped
    volumes:
      - ./stella-data:/home/stella/.stella
      - /var/run/docker.sock:/var/run/docker.sock
    environment:
      - STELLA_DOCKER_SANDBOX_MODE=bind
      - STELLA_HOME_HOST=${PWD}/stella-data
```

**Container with `docker` sandbox and a named volume:**

```yaml
services:
  stella:
    image: ghcr.io/cherryhq/stella:latest
    restart: unless-stopped
    volumes:
      - stella-data:/home/stella/.stella
      - /var/run/docker.sock:/var/run/docker.sock
    environment:
      - STELLA_DOCKER_SANDBOX_MODE=volume
      - STELLA_HOME_VOLUME=stella-data

volumes:
  stella-data:
```

### Environment Variables

| Variable                     | Description                                                               |
| ---------------------------- | ------------------------------------------------------------------------- |
| `STELLA_DOCKER_SANDBOX_MODE` | Required for the `docker` sandbox backend: `host`, `bind`, or `volume`    |
| `STELLA_HOME_HOST`           | Host-side path backing `STELLA_HOME`; required only in `bind` mode        |
| `STELLA_HOME_VOLUME`         | Docker named volume backing `STELLA_HOME`; required only in `volume` mode |

If agents use `local` or `none`, none of these variables are needed.

## Local Backend

The local backend runs commands directly on the host OS. It is intended for environments where Docker is unavailable or undesirable.

**This backend does not provide container-level isolation.** It applies OS-level hardening instead:

| Platform | Isolation                                                                                                                   |
| -------- | --------------------------------------------------------------------------------------------------------------------------- |
| Linux    | `bwrap` (bubblewrap) — mandatory. Minimal Linux root with `/workspace` read-write, scoped `/tmp`, network namespace control |
| macOS    | No additional sandboxing. Commands run directly on the host                                                                 |
| Windows  | Not supported. Use the Docker backend                                                                                       |

### Installing bubblewrap (Linux)

```bash
# Debian / Ubuntu
apt install bubblewrap

# Fedora / RHEL
dnf install bubblewrap

# Arch
pacman -S bubblewrap
```

bubblewrap must be functional, not just installed. Inside Docker containers without `--privileged` or `seccomp=unconfined`, the kernel seccomp profile blocks namespace creation — use the Docker backend in that environment instead.

### Path Presentation

Isolating backends (Linux `local` via bubblewrap, and `docker`) present a fixed **two-root** layout, independent of the real host paths:

| Sandbox path  | Backed by                       | Access     | Holds                                                            |
| ------------- | ------------------------------- | ---------- | ---------------------------------------------------------------- |
| `/workspace`  | the agent's per-agent directory | read-write | `$HOME` and the project working tree — private to this one agent |
| `/user`       | the user's shared data root     | read-write | data shared across all of that user's agents (see below)         |
| `/opt/stella` | the system install tree         | read-only  | system binaries, the shared mise toolchain, and system skills    |

Only the `bin`, `.mise-tools`, and `.agents/skills` subtrees of the system tree are mounted at `/opt/stella` — the sibling `users/` and `agents/` trees under `STELLA_HOME` are never exposed there.

The Linux `local` backend mounts a per-principal temp directory as sandbox `/tmp`; Docker `bind`/`host` mode does the same when that directory is visible to the Docker daemon, while Docker `volume` mode does not mount the host temp dir. Temp files thus stay scoped per user. On macOS (`local`) and with the `none` backend there is no remapping — the agent sees the real host paths, so these two roots are presented at their real locations rather than at `/workspace` and `/user`.

### Home directory and shared data

Inside an isolating sandbox, `$HOME` is `/workspace` — the agent's **own per-agent** directory. Each agent's credential and state directories (the XDG dirs `~/.config`, `~/.local/share`, `~/.local/state`, e.g. `~/.config/gh`) live there and stay private to that one agent. The project tree is the working directory, also under `/workspace`.

Data that should be shared across all of a user's agents lives under `/user` (the shared user-data root), exposed as `$STELLA_USER_DIR`: caches, user-scoped skills, and uploaded assets (`/user/assets`). On the Linux `local` backend the per-user mise toolchain also lives here, at `/user/.mise-tools`. Reference this root as `$STELLA_USER_DIR/...` rather than hardcoding `/user`, so the same instruction works on the `none`/macOS backends where it resolves to the real host path.

The docker backend bakes its mise toolchain under absolute `/opt/stella` — the same path the Linux `local` backend remaps `STELLA_HOME` to — so an agent sees identical mise paths whichever isolating backend runs it, and switching backends has no effect on tool resolution. `MISE_DATA_DIR` and friends are pinned to that tree, so flipping `$HOME` to `/workspace` does not hide the baked-in tools. The image installs its builtins through the same `resources/tools.yaml` reconcile the host runs, and the per-user writable mise tree is mounted at `/opt/stella/users/{id}/.mise-tools` so an agent can install its own tools on top of the shared base.

## None Backend

The `none` backend runs the agent directly on the host with the current user's permissions. No isolation of any kind — no filesystem confinement, no network restrictions, no process group kill, no resource limits.

**Use only for fully trusted agents in single-user local deployments.** Not safe for untrusted agents or multi-user environments.

- No external dependencies — works on all platforms.
- Network policy is always `allow_all`; configured per-agent network mode is ignored.
- Session creation never fails due to missing tooling.

## Network Policy

Each agent independently controls whether its sandbox allows outbound network access. Configure this from the agent's sandbox settings in the Web UI.

| Mode        | Description                          |
| ----------- | ------------------------------------ |
| `disabled`  | No outbound network access (default) |
| `allow_all` | Unrestricted outbound access         |

Docker and the Linux local backend validate the configured mode at session-create time and fail if the backend cannot enforce it. The macOS local backend currently ignores network policy.

## Troubleshooting

**bubblewrap fails inside a Docker container:**
The kernel seccomp profile blocks namespace creation. Add `--security-opt seccomp=unconfined` to your Docker run command or compose file. Alternatively, switch to the `docker` sandbox backend.

**Docker daemon not reachable:**
Session creation fails and the runner does not start. Ensure the Docker daemon is running and the socket is accessible. When running stellad in Docker, mount `/var/run/docker.sock`.

**Volume mode: "workspace is not inside STELLA_HOME":**
All sandbox workspaces must be subdirectories of `STELLA_HOME` in volume mode. This error means a workspace path was resolved outside the volume boundary. Check that `STELLA_HOME` and `STELLA_HOME_VOLUME` are correctly configured.

**Xberg fails to load `libheif`:**
Stella's Docker images include the compatible library. Native Linux deployments need libheif 1.21 or newer; Debian 13's package is too old. On macOS, install it with `brew install libheif`. If you cannot provide a compatible native library, use the Docker sandbox backend.

**Bind-mount performance is slow on macOS/Windows:**
Docker Desktop uses a virtualized filesystem layer for bind mounts. For heavy I/O workloads, consider using a named volume (`volume` mode) or running stellad natively on the host with `host` mode.
