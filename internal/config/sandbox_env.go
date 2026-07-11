package config

import (
	"os"
	"strings"
)

const sandboxBackendEnv = "STELLA_SANDBOX_BACKEND"

// SandboxBackendEnvOverride returns the env-forced sandbox backend name,
// or "" when the operator has not set STELLA_SANDBOX_BACKEND.
//
// This read is deliberately per-call and lenient (an unknown value means "no
// override"), so it stays outside ServerConfig; see the allowlist entry in
// env_scan_test.go.
func SandboxBackendEnvOverride() string {
	v := strings.TrimSpace(os.Getenv(sandboxBackendEnv))
	switch v {
	case SandboxBackendDocker, SandboxBackendLocal, SandboxBackendNone:
		return v
	default:
		return ""
	}
}
