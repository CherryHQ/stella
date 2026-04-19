# Anna sandbox image
#
# Build:
#   mise run sandbox:docker:build
#
# Use in config:
#   sandbox:
#     backend: docker
#     docker:
#       image: anna-sandbox

FROM debian:13-slim

COPY --from=jdxcode/mise:2026.4.17 /usr/local/bin/mise /usr/local/bin/mise
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
    mkdir -p /home/anna/workspace && \
    chown -R anna:anna /home/anna

USER anna
ENV HOME=/home/anna
ENV BASH_ENV=/home/anna/.bash_env
WORKDIR /home/anna/workspace

# Write shell env before copying config so mise activate is available on login.
RUN printf 'eval "$(mise activate bash)"\nexport PATH="/home/anna/.local/bin:$PATH"\n' \
    > /home/anna/.bash_env && \
    cp /home/anna/.bash_env /home/anna/.bashrc

COPY --chown=anna:anna _mise.toml /home/anna/.config/mise/config.toml

RUN --mount=type=secret,id=github_token,env=GITHUB_TOKEN \
    mise install && \
    mise cache clear
