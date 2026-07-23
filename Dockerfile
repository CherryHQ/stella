# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM ghcr.io/pnpm/pnpm:latest AS web-builder
RUN pnpm runtime set node 24 -g
WORKDIR /web
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY api/spec/ /api/spec/
COPY web/ ./
RUN pnpm openapi-ts && CI=true pnpm build

# Xberg's GNU release links libheif dynamically. Debian 13's packaged version
# is too old, so build the upstream-supported 1.23.0 locally for each target.
FROM debian:13-slim AS libheif-builder

RUN apt-get update \
    && apt-get -y --no-install-recommends install \
        ca-certificates cmake curl g++ make \
        libaom-dev libde265-dev libx265-dev \
    && rm -rf /var/lib/apt/lists/* \
    && curl -fsSL -o /tmp/libheif.tar.gz "https://github.com/strukturag/libheif/releases/download/v1.23.0/libheif-1.23.0.tar.gz" \
    && echo "4c9182b18897617182eed12ab5eb9f9d855b3aa3a736d6bdb31abc034ec7d393  /tmp/libheif.tar.gz" | sha256sum --check - \
    && tar -xzf /tmp/libheif.tar.gz -C /tmp \
    && cmake -S /tmp/libheif-1.23.0 -B /tmp/libheif-build \
        -DCMAKE_BUILD_TYPE=Release \
        -DCMAKE_INSTALL_PREFIX=/usr/local \
        -DCMAKE_INSTALL_LIBDIR=lib \
        -DWITH_EXAMPLES=OFF \
        -DWITH_GDK_PIXBUF=OFF \
        -DBUILD_TESTING=OFF \
    && cmake --build /tmp/libheif-build -j"$(nproc)" \
    && cmake --install /tmp/libheif-build

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

COPY --from=libheif-builder /usr/local/lib/libheif.so.1.23.0 /usr/local/lib/

RUN apt-get update \
    && apt-get -y --no-install-recommends install \
        ca-certificates libncurses6 libstdc++6 \
        libaom3 libde265-0 libx265-215 \
        bubblewrap util-linux \
    && ldconfig \
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
