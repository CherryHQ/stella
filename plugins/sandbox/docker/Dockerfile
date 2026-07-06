# syntax=docker/dockerfile:1
# Stella sandbox image
#
# Build locally (dev):
# mise run sandbox:docker:build
#
# The image tag is version-locked to the stella version used at build time:
# - dev builds      → stella-sandbox:dev            (built locally)
# - tagged releases → ghcr.io/vaayne/stella-sandbox:<version>

# Cross-compile from the build host's arch (BUILDPLATFORM) to the target arch so
# buildx multi-arch builds don't run the Go toolchain under QEMU. TARGETOS/ARCH
# are auto-populated by BuildKit (default to the host arch for a plain build).
FROM --platform=$BUILDPLATFORM golang:1.25 AS stella-builder

WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -ldflags="-X github.com/CherryHQ/stella/internal/version.Version=${VERSION}" \
    -o /out/stella ./cmd/stella

FROM debian:13-slim

# mise lives at /opt/stella/bin/mise — the same path the bwrap sandbox exposes —
# so per-user and system shims (which relink to ../../bin/mise) resolve to it.
# bun, uv (+uvx), fd, rg, and lightpanda are NOT vendored here: `stella mise
# reconcile-builtins` below installs them from resources/tools.yaml, exposing
# them via mise shims exactly as the host does. Only non-mise base runtime deps
# are installed via apt.
COPY --from=jdxcode/mise:2026.4.25 /usr/local/bin/mise /opt/stella/bin/mise

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        ca-certificates git curl wget \
        python3 python3-venv \
        nodejs npm && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

ARG USER_UID=1000
ARG USER_GID=1000

RUN groupadd --non-unique -g ${USER_GID} stella && \
    useradd --non-unique -u ${USER_UID} -g ${USER_GID} -m -s /bin/bash stella && \
    mkdir -p /workspace /user /opt/stella/bin /opt/stella/.mise-tools/configs && \
    chown -R stella:stella /home/stella /workspace /user /opt/stella

USER stella
# The image mise tree lives under ABSOLUTE /opt/stella — the SAME scheme the
# bwrap sandbox exposes (STELLA_HOME → /opt/stella) — so an agent sees identical
# mise paths whichever backend runs it, and switching backends has no effect. At
# runtime RuntimeMiseEnv overrides MISE_DATA_DIR to the per-user writable tree
# (/opt/stella/users/{id}/.mise-tools) mounted from the host; the builtin tools
# baked here under /opt/stella/.mise-tools remain the shared read-only base that
# per-user relative symlinks resolve against. MISE_GLOBAL_CONFIG_FILE points at
# the _builtin config the runtime also resolves against (RuntimeMiseEnv →
# ScopeConfigPath(_builtin)). HOME stays /home/stella for the build, then flips
# to /workspace.
ENV HOME=/home/stella
ENV MISE_DATA_DIR=/opt/stella/.mise-tools
ENV MISE_CONFIG_DIR=/opt/stella/.mise-tools/config
ENV MISE_STATE_DIR=/opt/stella/.mise-tools/state
ENV MISE_CACHE_DIR=/opt/stella/.mise-tools/cache
ENV MISE_GLOBAL_CONFIG_FILE=/opt/stella/.mise-tools/configs/_builtin.toml
ENV PATH=/opt/stella/.mise-tools/shims:/opt/stella/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

# Install the builtin tools through the SAME reconcile path the daemon runs, so
# the image's tool identifiers and versions are byte-for-byte what the host
# produces from resources/tools.yaml (single source of truth, no drift). The
# stella CLI is bind-mounted only for this build step; it is not shipped in the
# runtime image. This writes /opt/stella/.mise-tools/configs/_builtin.toml,
# installs every enabled plugin binary, and relinks shims to relative targets.
RUN --mount=type=bind,from=stella-builder,source=/out/stella,target=/usr/local/bin/stella \
    --mount=type=secret,id=github_token,env=GITHUB_TOKEN \
    STELLA_HOME=/opt/stella stella mise reconcile-builtins && \
    rm -rf /opt/stella/.mise-tools/cache

ENV HOME=/workspace
WORKDIR /workspace
