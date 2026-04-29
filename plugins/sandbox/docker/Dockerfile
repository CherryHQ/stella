# Anna sandbox image
#
# Build locally (dev):
#   mise run sandbox:docker:build
#
# The image tag is version-locked to the anna binary:
#   - dev builds      → anna-sandbox:dev            (built locally)
#   - tagged releases → ghcr.io/vaayne/anna-sandbox:<version>

FROM debian:13-slim

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

RUN groupadd --non-unique -g ${USER_GID} anna && \
    useradd --non-unique -u ${USER_UID} -g ${USER_GID} -m -s /bin/bash anna && \
    mkdir -p /home/anna/workspace /home/anna/readonly && \
    chown -R anna:anna /home/anna

USER anna
ENV HOME=/home/anna
ENV PATH=/home/anna/.local/bin:/home/anna/.local/share/mise/shims:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
WORKDIR /home/anna/workspace

COPY --chown=anna:anna _mise.toml /home/anna/.config/mise/config.toml

RUN --mount=type=secret,id=github_token,env=GITHUB_TOKEN \
    mise install && \
    mise cache clear
