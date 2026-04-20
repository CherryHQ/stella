// Package dockerclient wraps the moby Go SDK (github.com/moby/moby/client) to
// manage sandbox containers for the docker sandbox backend. The client is
// configured from the process environment (DOCKER_HOST, DOCKER_TLS_VERIFY,
// DOCKER_CERT_PATH, DOCKER_API_VERSION) via client.FromEnv; DOCKER_CONTEXT is
// intentionally not supported — it's a CLI-only concept.
package dockerclient
