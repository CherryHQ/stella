# syntax=docker/dockerfile:1
# Stella sandbox image
#
# Build locally (dev):
# mise run sandbox:docker:build
#
# The image tag is version-locked to the stella version used at build time:
# - dev builds      → stella-sandbox:dev            (built locally)
# - tagged releases → ghcr.io/cherryhq/stella-sandbox:<version>

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
RUN TARGET_GOOS=${TARGETOS} TARGET_GOARCH=${TARGETARCH} go generate ./resources/binaries/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -ldflags="-X github.com/CherryHQ/stella/internal/version.Version=${VERSION}" \
    -o /out/stellad ./cmd/stellad

FROM debian:13-slim

ARG BUILTIN_BUNDLE_REVISION
LABEL org.cherryhq.stella.builtin-bundle-revision=${BUILTIN_BUNDLE_REVISION}

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
    mkdir -p /workspace /user /opt/stella/bin /opt/stella/.mise-tools/configs /opt/stella/user-tools && \
    ln -s /opt/stella/bin/.stella-shell-env /etc/profile.d/99-stella-mise.sh && \
    chown -R stella:stella /home/stella /workspace /user /opt/stella

USER stella
# The image mise tree lives under ABSOLUTE /opt/stella — the SAME scheme the
# bwrap sandbox exposes (STELLA_HOME → /opt/stella) — so an agent sees identical
# mise paths whichever backend runs it, and switching backends has no effect. At
# runtime RuntimeMiseEnv overrides MISE_DATA_DIR to the per-user writable tree
# (/opt/stella/users/{id}/.mise-tools) mounted from the host; the builtin tools
# baked here under /opt/stella/.mise-tools remain the shared read-only base that
# per-user relative symlinks resolve against. MISE_SYSTEM_CONFIG_FILE points at
# the release-owned _builtin config; runtime may add a principal-global config
# above it without making the system file writable. HOME stays /home/stella for
# the build, then flips to /workspace.
ENV HOME=/home/stella
ENV BASH_ENV=/opt/stella/bin/.stella-shell-env
ENV MISE_DATA_DIR=/opt/stella/.mise-tools
ENV MISE_CONFIG_DIR=/opt/stella/.mise-tools/config
ENV MISE_STATE_DIR=/opt/stella/.mise-tools/state
ENV MISE_CACHE_DIR=/opt/stella/.mise-tools/cache
ENV MISE_SYSTEM_CONFIG_FILE=/opt/stella/.mise-tools/configs/_builtin.toml
ENV PATH=/opt/stella/bin:/opt/stella/.mise-tools/shims:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

# `system-bundle install` extracts the exact release-owned mise, Xberg, and
# shell environment before the reconciler invokes mise. This avoids a second,
# image-provided mise version and lets relative system/user shims resolve at the
# same /opt/stella/bin path bwrap exposes. The stellad is bind-mounted only for
# this build step; it is not shipped in the runtime image. Reconcile then writes
# the builtin config, installs enabled plugin binaries, and relinks shims.
RUN --mount=type=bind,from=stella-builder,source=/out/stellad,target=/usr/local/bin/stellad \
    --mount=type=secret,id=github_token,env=GITHUB_TOKEN \
	STELLA_HOME=/opt/stella stellad system-bundle install && \
	STELLA_HOME=/opt/stella stellad mise reconcile-builtins && \
	REVISION="$(STELLA_HOME=/opt/stella stellad system-bundle revision)" && \
	test "$REVISION" = "$BUILTIN_BUNDLE_REVISION" && \
	mkdir -p /opt/stella/skills && ln -s "../bundles/$REVISION" /opt/stella/skills/builtin && \
    xberg --version && \
    rm -rf /opt/stella/.mise-tools/cache

# The install above runs as the owning UID, so an owner-only permission bit on a
# bundled runtime cannot fail it — that is how a 0700 Xberg bundle directory once
# shipped while `xberg --version` passed here. USER_UID is a build arg and the
# image is routinely run under a different UID, so re-run every bundled runtime
# as an unrelated one (nobody), plus one mise shim: reconcile-builtins writes the
# shim tree as the same installing UID, onto the same PATH, with the same
# exposure. This is the only check that distinguishes "installed" from "reachable
# by the process that will actually use it".
USER root
RUN HOME=/tmp setpriv --reuid=65534 --regid=65534 --clear-groups mise --version && \
    HOME=/tmp setpriv --reuid=65534 --regid=65534 --clear-groups xberg --version && \
    HOME=/tmp setpriv --reuid=65534 --regid=65534 --clear-groups rg --version
USER stella

ENV HOME=/workspace
WORKDIR /workspace
