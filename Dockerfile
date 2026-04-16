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

# Copy source code.
COPY . .

ARG TARGETOS TARGETARCH
ARG VERSION=dev

# Generate code, fetch embedded runtime tools for the target platform, then cross-compile.
RUN templ generate
RUN sqlc generate
RUN --mount=type=secret,id=github_token,required=false \
    export GITHUB_TOKEN="$(cat /run/secrets/github_token 2>/dev/null || true)" \
    && bash --noprofile --norc ./scripts/download-tools.sh --goos ${TARGETOS} --goarch ${TARGETARCH}
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags "-s -w -X main.version=${VERSION}" -o bin/anna ./cmd/anna/

FROM gcr.io/distroless/static-debian13:nonroot AS app
WORKDIR /workspace

COPY --from=builder /go/src/app/bin/anna .

USER nonroot:nonroot

CMD ["./anna", "gateway"]
