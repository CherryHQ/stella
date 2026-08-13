---
title: Sandbox
---

Stella runs agent code inside a sandbox. The backend is a deployment-wide choice made by the operator — each backend offers different isolation levels, platform support, and tradeoffs.

## Choosing a Backend

| Scenario                                   | Recommended | Why                                                             |
| ------------------------------------------ | ----------- | --------------------------------------------------------------- |
| Production / multi-user                    | `docker`    | Full container-level process, filesystem, and network isolation |
| Linux without Docker                       | `local`     | OS-level isolation via bubblewrap                               |
| macOS without Docker                       | `none`      | `local` provides no extra isolation on macOS                    |
| Native Windows host                        | —           | Native `stellad` servers are not supported                      |
| Trusted single-user local dev              | `none`      | Zero dependencies, no isolation                                 |
| Custom toolchain (specific Python/Node/Go) | `docker`    | Clean Linux userspace independent of host                       |

Set the backend with the `STELLA_SANDBOX_BACKEND` environment variable when you deploy, then restart `stellad`:

```bash
STELLA_SANDBOX_BACKEND=docker   # docker | local | none
```

The default is `local`. An unset or unrecognized value also resolves to `local`, so a typo never leaves agents unisolated. There is no Web UI or per-agent override — the sandbox boundary is an operator decision, not a runtime one.

## Docker Backend

Docker provides full container-level process, filesystem, and network isolation. The Docker daemon must be running and reachable on the supported Linux or macOS server host.

### When to Use

- You need strong isolation between the agent and the host.
- You need a reproducible Linux environment with a specific toolchain.
- You want side-effect isolation — agent scripts cannot modify the host filesystem outside the mounted workspace.

### Tradeoffs

- **Startup latency**: ~200ms for a warm container start; ~1–3s on first pull.
- **Bind-mount performance**: On Docker Desktop for macOS, bind-mount filesystem operations are 5–20× slower than native disk. Avoid it for heavy read/write workflows.
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

| Variable                     | Description                                                                |
| ---------------------------- | -------------------------------------------------------------------------- |
| `STELLA_SANDBOX_BACKEND`     | Sandbox backend for the deployment: `docker`, `local` (default), or `none` |
| `STELLA_DOCKER_SANDBOX_MODE` | Required for the `docker` sandbox backend: `host`, `bind`, or `volume`     |
| `STELLA_HOME_HOST`           | Host-side path backing `STELLA_HOME`; required only in `bind` mode         |
| `STELLA_HOME_VOLUME`         | Docker named volume backing `STELLA_HOME`; required only in `volume` mode  |

If agents use `local` or `none`, none of these variables are needed.

## Local Backend

The local backend runs commands directly on the host OS. It is intended for environments where Docker is unavailable or undesirable.

**This backend does not provide container-level isolation.** It applies OS-level hardening instead:

| Platform | Isolation                                                                                                                   |
| -------- | --------------------------------------------------------------------------------------------------------------------------- |
| Linux    | `bwrap` (bubblewrap) — mandatory. Minimal Linux root with `/workspace` read-write, scoped `/tmp`, network namespace control |
| macOS    | No additional sandboxing. Commands run directly on the host                                                                 |
| Windows  | Native `stellad` servers are not supported                                                                                  |

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

### Agent filesystem contract

Use these environment variables in Agent instructions. They are the filesystem API for Agent work; literal sandbox paths are only backend rendering, compatibility, or command-output details. The `read`, `write`, and `edit` tools understand all three roots. `share` accepts `$HOME` and `$STELLA_ASSETS_DIR`, but not `$TMPDIR`. Never hardcode `/workspace`, `/user`, or `/tmp` in Agent instructions.

| Root                 | Use                                                                                     | Rules                                                                         |
| -------------------- | --------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------- |
| `$HOME`              | Durable, private per-Agent workspace for project and default work                       | Relative paths resolve in the current project/work directory.                 |
| `$STELLA_ASSETS_DIR` | Durable uploads and final deliverables shared by the same user or group, when available | This is the managed shared location for direct Agent writes.                  |
| `$TMPDIR`            | Session-private disposable scratch space                                                | Never put final output here or rely on it surviving after the session closes. |

The Web Workspace API addresses files with a typed scope plus canonical relative paths. Project `base_dir` values are likewise relative to the Agent workspace (`.` is its root). These APIs authorize and open the durable POSIX root directly; they do not start or wake Session compute. The returned `root` is the logical `/workspace` or `/user` root and never contains a host path. Active Agent tools still resolve paths through their existing Session mount and policy boundary.

### Managed user and group roots

`XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_STATE_HOME`, and `XDG_CACHE_HOME` are shared by the same user's or group's Agents and managed for command-line tools. They are not general Agent storage. If no user/group root is available, all four fall back under `$HOME`. `XDG_RUNTIME_DIR` is unset.

Because these XDG directories are principal-shared, an on-disk CLI login or configuration — potentially including credentials — is visible to every Agent for that same principal. For per-Agent authentication, store the credential in an Agent-specific [Vault scope](/docs/guides/secrets-and-keys) and use the CLI's environment-based authentication instead of a persistent login.

Mise, Lark, and system directories are tool-managed, not generic storage locations.

### Backend path rendering

The following literal paths describe a process view, not the Agent filesystem API:

| Backend or condition       | Process view                                                                                   |
| -------------------------- | ---------------------------------------------------------------------------------------------- |
| Linux `local` and `docker` | Normally `$HOME=/workspace`, `$STELLA_ASSETS_DIR=/user/assets`, and `$TMPDIR=/tmp`.            |
| macOS `local` and `none`   | The process sees the actual host paths rather than remapped sandbox paths.                     |
| Docker without `/user`     | `$STELLA_ASSETS_DIR` is absent, and the XDG directories fall back under `$HOME`/the workspace. |

Every backend creates a private temporary directory for each sandbox session and removes it when the session closes. Docker stores its backing directory under `$STELLA_HOME/cache/sandbox-tmp/` and mounts it at `/tmp`, so shell commands and file tools access the same content; startup cleanup removes stale Docker directories. This is scratch space, not a durability promise.

Isolating backends also render the system install tree at `/opt/stella` as read-only. Its tool-managed `bin` and `.mise-tools` trees remain available, and builtins appear at `/opt/stella/skills/builtin`; the sibling `users/` and `agents/` trees under `STELLA_HOME` are not exposed. A selected managed Skill is copied separately into a digest-pinned, session-private directory under `$TMPDIR`; its complete authority root and revision history are never mounted into the sandbox. The Docker backend bakes its mise toolchain at `/opt/stella`, and Linux `local` renders the matching system tree there, so tool resolution remains consistent across isolating backends. `MISE_DATA_DIR` and related variables stay pinned to that tool-managed tree.

### Builtin Skill bundle

Native `local` and `none` installs use the exact release bundle at `$STELLA_HOME/bundles/<revision>`. The read-only `/opt/stella/skills/builtin` mount is only the isolating execution view, never a second authority. Helper executable modes in the bundle are preserved.

The Docker sandbox image bakes and labels the same revision. It does not fall back to host builtins. Docker provider preflight rejects a revision mismatch, so the runner session does not start. For command syntax, run `stellad system-bundle --help`. Developers rebuilding the local sandbox image run `mise run sandbox:docker:build`; custom sandbox images must be rebuilt from the matching Stella revision.

Before upgrading, use the old working binary to import each custom Skill root under legacy `$STELLA_HOME/.agents/skills` as a global (`system`) Skill through **Settings → Skills** on older releases or **Admin Console → Deployment resources → Global Skills** on newer releases. Back up, verify, and remove other residual paths. Startup lists every blocking path and stops without deleting or changing anything. Paths owned by the current release manifest are inert even when their contents or modes are stale; every other Skill root or residual path blocks startup.

### Upgrading existing workspaces

Existing Agent-local XDG files are not moved, merged, or deleted during upgrade. New CLI state is principal-shared rather than Agent-private, so persistent CLI logins created after upgrading are visible to the same user’s or group’s other Agents. XDG-aware command-line tools may need one setup or login for that user or group after upgrading. Perform a tool-specific manual migration only when you understand that tool's data; caches can repopulate.

`STELLA_USER_DIR` is removed rather than retained as a compatibility path. Replace Agent instructions and share expressions that used it with `$STELLA_ASSETS_DIR` for shared deliverables or `$HOME` for private Agent work.

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

**Xberg is unavailable after upgrading:**
Current Linux and macOS releases bundle Xberg and its native libraries. Restart Stella with the upgraded `stellad` binary so it can install the matching runtime into `STELLA_HOME`; do not install a separate `libheif` package.

**Bind-mount performance is slow on macOS/Windows:**
Docker Desktop uses a virtualized filesystem layer for bind mounts. For heavy I/O workloads, consider using a named volume (`volume` mode) or running stellad natively on the host with `host` mode.
