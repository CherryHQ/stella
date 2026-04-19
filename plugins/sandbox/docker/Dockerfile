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

COPY --from=jdxcode/mise:latest /usr/local/bin/mise /usr/local/bin/mise
COPY --from=ghcr.io/astral-sh/uv:latest /uv /uvx /usr/local/bin/
COPY --from=oven/bun:1 /usr/local/bin/bunx /usr/local/bin/bun /usr/local/bin/

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
    bash ca-certificates curl diffutils file findutils \
    git jq make patch python3 ripgrep fd-find unzip wget && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

ARG USER_UID=1000
ARG USER_GID=1000

RUN groupadd --non-unique -g ${USER_GID} dev && \
    useradd --non-unique -u ${USER_UID} -g ${USER_GID} -m -s /bin/bash dev && \
    mkdir -p /home/dev/workspace && \
    chown -R dev:dev /home/dev

USER dev
ENV HOME=/home/dev
ENV BASH_ENV=/home/dev/.bash_env
WORKDIR /home/dev/workspace

# Write shell env before copying config so mise activate is available on login.
RUN printf 'eval "$(mise activate bash)"\nexport PATH="/home/dev/.local/bin:$PATH"\n' \
    > /home/dev/.bash_env && \
    cp /home/dev/.bash_env /home/dev/.bashrc

COPY --chown=dev:dev _mise.toml /home/dev/.config/mise/config.toml

RUN --mount=type=secret,id=github_token,env=GITHUB_TOKEN \
    mise install && \
    mise cache clear
