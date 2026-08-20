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
	case SandboxBackendDocker, SandboxBackendLocal, SandboxBackendNone, SandboxBackendBridge:
		return v
	default:
		return SandboxBackendLocal
	}
}

// evalBridgeBindingDirEnv names the directory where an evaluation harness
// publishes per-user bridge bindings for the bridge sandbox backend.
const evalBridgeBindingDirEnv = "STELLA_EVAL_BRIDGE_DIR"

// EvalBridgeBindingDir returns the bridge binding directory. Like the backend
// name it is read per session creation: it is evaluation-only plumbing, never a
// ServerConfig field.
func EvalBridgeBindingDir() string {
	return strings.TrimSpace(os.Getenv(evalBridgeBindingDirEnv))
}
