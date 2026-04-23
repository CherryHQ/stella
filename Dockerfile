# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM debian:13-slim AS builder

WORKDIR /go/src/app

# Install prerequisites
RUN apt-get update \
    && apt-get -y --no-install-recommends install \
        bash curl ca-certificates git \
    && rm -rf /var/lib/apt/lists/*

SHELL ["/bin/bash", "-o", "pipefail", "-c"]
ENV MISE_INSTALL_PATH="/usr/local/bin/mise"
ENV CGO_ENABLED=0

# Install mise, then install Go with mise so the toolchain matches project configuration.
RUN curl -fsSL https://mise.run | sh
RUN mise install go@1.25 \
    && GO_ROOT="$(mise where go@1.25)" \
    && ln -sf "$GO_ROOT/bin/go" /usr/local/bin/go \
    && ln -sf "$GO_ROOT/bin/gofmt" /usr/local/bin/gofmt

# Install codegen CLIs needed to produce a fresh build from source.
COPY go.mod go.sum ./
RUN go mod download
RUN GOBIN=/usr/local/bin go install github.com/a-h/templ/cmd/templ@v0.3.1001
RUN GOBIN=/usr/local/bin go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.29.0

# Copy source code before syncing build dependencies. The builddeps tool now
# depends on plugin host and plugin packages outside cmd/builddeps/internal/builddeps,
# so partial copies here are brittle and break Docker builds when that graph grows.
ARG TARGETOS TARGETARCH
ARG VERSION=dev
COPY . .
RUN --mount=type=secret,id=github_token,required=false \
    export GITHUB_TOKEN="$(cat /run/secrets/github_token 2>/dev/null || true)" \
    && go run ./cmd/builddeps sync --skills --tools --goos ${TARGETOS} --goarch ${TARGETARCH}

# Generate code, fetch embedded runtime tools for the target platform, then cross-compile.
RUN templ generate
RUN sqlc generate

RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags "-s -w -X github.com/vaayne/anna/internal/version.Version=${VERSION}" -o bin/anna ./cmd/anna/

FROM debian:13-slim AS app

RUN apt-get update \
    && apt-get -y --no-install-recommends install \
        ca-certificates libncurses6 libstdc++6 \
        bubblewrap util-linux \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /home/nonroot /workspace \
    && chown -R 65532:65532 /home/nonroot /workspace

WORKDIR /workspace
ENV HOME="/home/nonroot"
ENV PATH="/usr/local/bin:/usr/bin:/bin"

COPY --from=builder /go/src/app/bin/anna /usr/local/bin/anna

USER 65532:65532

CMD ["anna", "gateway"]
