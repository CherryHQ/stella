# syntax=docker/dockerfile:1.7

# Built through mise rather than a Node base image so the Node and global Vite+
# versions come from mise.toml, the same place local and CI builds read them
# from. Vite+ resolves pnpm from web/package.json.
FROM --platform=$BUILDPLATFORM debian:13-slim AS web-builder
RUN apt-get update \
    && apt-get -y --no-install-recommends install ca-certificates curl git libatomic1 \
    && rm -rf /var/lib/apt/lists/*
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
ENV MISE_INSTALL_PATH="/usr/local/bin/mise"
RUN curl -fsSL https://mise.run | sh
WORKDIR /src
# The whole mise config, not just mise.toml: task bodies live in .mise/tasks/,
# and a stage that copies only the TOML fails on the first file task it runs.
COPY mise.toml ./
COPY .mise/ ./.mise/
RUN mise trust && mise install node vp
COPY api/spec/ ./api/spec/
COPY web/ ./web/
ENV CI=true
# The SPA build needs Node and Vite+ and nothing else; without this mise would
# also fetch Go, sqlc, and GoReleaser here just to satisfy mise.toml.
ENV MISE_TASK_RUN_AUTO_INSTALL=0
RUN --mount=type=cache,target=/root/.local/share/pnpm/store \
    mise run build:web

FROM --platform=$BUILDPLATFORM golang:1.25-trixie AS builder

WORKDIR /go/src/app

# download go mods
COPY go.mod go.sum ./
RUN go mod download

# install mise
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
ENV MISE_INSTALL_PATH="/usr/local/bin/mise"
RUN curl -fsSL https://mise.run | sh
COPY mise.toml mise.toml
COPY .mise/ ./.mise/
RUN mise trust

# Build app
COPY . .
COPY --from=web-builder /src/web/static/dist/ ./web/static/dist/

ENV CGO_ENABLED=0
# STRIP=1: the image has no delve, so the symbol and DWARF tables are ~35 MB
# every pull carries and nothing reads. Panic tracebacks are unaffected.
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
RUN --mount=type=secret,id=github_token \
    --mount=type=cache,target=/root/.local/share/mise \
    --mount=type=cache,target=/root/.cache \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    if [ -f /run/secrets/github_token ]; then export GITHUB_TOKEN="$(cat /run/secrets/github_token)"; fi; \
    env -u GOOS -u GOARCH \
      TARGET_GOOS=${TARGETOS} TARGET_GOARCH=${TARGETARCH} STRIP=1 \
      mise run build

FROM debian:13-slim AS app

RUN apt-get update \
    && apt-get -y --no-install-recommends install \
        ca-certificates libncurses6 libstdc++6 \
        bubblewrap util-linux \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --create-home --home-dir /home/stella --shell /usr/sbin/nologin stella \
    && install -d -o stella -g stella /home/stella/.stella

WORKDIR /home/stella
USER stella
COPY --from=builder /go/src/app/dist/bin/stellad /usr/local/bin/stellad

# Containers must bind all interfaces to be reachable; the --host flag binds to
# HOST, so set it via env instead of a hardcoded flag. This keeps the CMD free of
# an explicit --host so a manifest that overrides `command:` does not silently
# drop the bind, and lets users override HOST without fighting the flag.
ENV HOST=0.0.0.0
# In a container the embedded PostgreSQL cluster would land on an ephemeral
# filesystem (and split state across replicas), so the image requires an
# external STELLA_DATABASE_URL by default. Override with =0 to deliberately run
# embedded PostgreSQL on a persistent volume.
ENV STELLA_REQUIRE_EXTERNAL_DB=1
CMD ["stellad", "server"]
