# Stella sandbox image
#
# Build locally (dev):
#   mise run sandbox:docker:build
#
# The image tag is version-locked to the stella binary:
#   - dev builds      → stella-sandbox:dev            (built locally)
#   - tagged releases → ghcr.io/vaayne/stella-sandbox:<version>

FROM golang:1.25 AS stella-builder

WORKDIR /src
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-X github.com/CherryHQ/stella/internal/version.Version=${VERSION}" \
    -o /out/stella ./cmd/stella

FROM debian:13-slim

COPY --from=stella-builder /out/stella /usr/local/bin/stella
COPY --from=jdxcode/mise:2026.4.25 /usr/local/bin/mise /usr/local/bin/mise
COPY --from=ghcr.io/astral-sh/uv:0.11.7 /uv /uvx /usr/local/bin/
COPY --from=oven/bun:1.3.12 /usr/local/bin/bunx /usr/local/bin/bun /usr/local/bin/
COPY --from=lightpanda/browser:nightly /bin/lightpanda /usr/local/bin/lightpanda


RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        ca-certificates git curl wget \
        python3 python3-venv \
        nodejs npm \
        ripgrep fd-find && \
    ln -s /usr/bin/fdfind /usr/local/bin/fd && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

ARG USER_UID=1000
ARG USER_GID=1000

RUN groupadd --non-unique -g ${USER_GID} stella && \
    useradd --non-unique -u ${USER_UID} -g ${USER_GID} -m -s /bin/bash stella && \
    mkdir -p /home/stella/workspace /home/stella/readonly && \
    chown -R stella:stella /home/stella

USER stella
ENV HOME=/home/stella
ENV PATH=/home/stella/.local/bin:/home/stella/.local/share/mise/shims:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
WORKDIR /home/stella/workspace

COPY --chown=stella:stella plugins/sandbox/docker/_mise.toml /home/stella/.config/mise/config.toml

RUN --mount=type=secret,id=github_token,env=GITHUB_TOKEN \
    mise install && \
    mise cache clear
