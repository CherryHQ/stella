# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM node:22-slim AS web-builder

WORKDIR /web
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN npm install -g pnpm@11.0.8 --ignore-scripts && pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

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

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
COPY . .
COPY --from=web-builder /web/static/dist/ ./web/static/dist/

# Download embedded mise binary for the target platform.
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} go generate ./internal/resources/binaries/

# Generate code, fetch embedded runtime tools for the target platform, then cross-compile.
RUN templ generate
RUN sqlc generate

RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags "-s -w -X github.com/CherryHQ/stella/internal/version.Version=${VERSION}" -o bin/stella ./cmd/stella/

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

COPY --from=builder /go/src/app/bin/stella /usr/local/bin/stella

USER 65532:65532

CMD ["stella", "gateway"]
