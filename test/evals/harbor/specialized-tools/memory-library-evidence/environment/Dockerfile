FROM debian:bookworm-slim
RUN useradd --create-home --shell /bin/sh agent && mkdir /workspace && chown agent:agent /workspace && apt-get update && apt-get install -y --no-install-recommends openssl && rm -rf /var/lib/apt/lists/*
WORKDIR /workspace
USER agent
