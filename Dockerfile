# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM ghcr.io/pnpm/pnpm:latest AS web-builder
RUN pnpm runtime set node 24 -g
WORKDIR /web
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY api/spec/ /api/spec/
COPY web/ ./
RUN pnpm openapi-ts && CI=true pnpm build

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
RUN mise trust

# Build app
COPY . .
COPY --from=web-builder /web/static/dist/ ./web/static/dist/

ENV CGO_ENABLED=0
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
RUN --mount=type=secret,id=github_token \
    --mount=type=cache,target=/root/.local/share/mise \
    --mount=type=cache,target=/root/.cache \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    if [ -f /run/secrets/github_token ]; then export GITHUB_TOKEN="$(cat /run/secrets/github_token)"; fi; \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} mise run build

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
