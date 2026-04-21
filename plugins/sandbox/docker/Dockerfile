# Anna sandbox image
#
# Build locally (dev):
#   mise run sandbox:docker:build
#
# The image tag is version-locked to the anna binary:
#   - dev builds      → anna-sandbox:dev            (built locally)
#   - tagged releases → ghcr.io/vaayne/anna-sandbox:<version>

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

# GitHub CLI (gh) — version pinned to match internal/builddeps/binaries.go ghCLIVersion
RUN curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
        | dd of=/usr/share/keyrings/githubcli-archive-keyring.gpg && \
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
        | tee /etc/apt/sources.list.d/github-cli.list > /dev/null && \
    apt-get update && \
    apt-get install -y gh=2.89.0 && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

# lark-cli — version pinned to match internal/builddeps/binaries.go larkCLIVersion
# Released as a tarball from github.com/larksuite/cli at /usr/local/bin/lark-cli
# (injectDockerBinPaths in plugins/sandbox/docker/session.go relies on this path)
ARG LARK_CLI_VERSION=1.0.15
RUN set -eux; \
    ARCH="$(dpkg --print-architecture)"; \
    case "$ARCH" in \
        amd64) LARK_ARCH="linux-amd64" ;; \
        arm64) LARK_ARCH="linux-arm64" ;; \
        *) echo "unsupported architecture: $ARCH" >&2; exit 1 ;; \
    esac; \
    curl -fsSL \
        "https://github.com/larksuite/cli/releases/download/v${LARK_CLI_VERSION}/lark-cli-${LARK_CLI_VERSION}-${LARK_ARCH}.tar.gz" \
        -o /tmp/lark-cli.tar.gz && \
    tar -xzf /tmp/lark-cli.tar.gz -C /tmp && \
    find /tmp -name "lark-cli" -type f -exec install -m 755 {} /usr/local/bin/lark-cli \; && \
    rm -f /tmp/lark-cli.tar.gz

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
