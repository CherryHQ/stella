package config

import (
	"os"
	"strings"
)

const sandboxBackendEnv = "STELLA_SANDBOX_BACKEND"

// ActiveSandboxBackend returns the sandbox backend the deployment runs on.
//
// The backend is a deploy-time decision owned by the operator through
// STELLA_SANDBOX_BACKEND; there is no runtime, per-user, or admin override. An
// unset or unrecognized value resolves to SandboxBackendLocal, so a typo
// degrades to a sandboxed default instead of leaving agents unisolated.
//
// The read is deliberately per-call and lenient, so it stays outside
// ServerConfig; see the allowlist entry in env_scan_test.go.
func ActiveSandboxBackend() string {
	switch v := strings.TrimSpace(os.Getenv(sandboxBackendEnv)); v {
	case SandboxBackendDocker, SandboxBackendBridge, SandboxBackendLocal, SandboxBackendNone:
		return v
	default:
		return SandboxBackendLocal
	}
}
