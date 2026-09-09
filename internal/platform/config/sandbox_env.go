package config

import (
	"fmt"
	"os"
	"strings"
)

const sandboxBackendEnv = "STELLA_SANDBOX_BACKEND"

// ActiveSandboxBackend returns the sandbox backend the deployment runs on.
//
// The backend is a deploy-time decision owned by the operator through
// STELLA_SANDBOX_BACKEND; there is no runtime, per-user, or admin override. An
// unset value resolves to local. Unknown explicit values fail startup validation.
func ActiveSandboxBackend() string {
	value := strings.TrimSpace(os.Getenv(sandboxBackendEnv))
	if value == "" {
		return SandboxBackendLocal
	}
	return value
}

func ValidateSandboxBackend() error {
	switch ActiveSandboxBackend() {
	case SandboxBackendDocker, SandboxBackendKubernetes, SandboxBackendLocal, SandboxBackendNone, SandboxBackendBridge:
		return nil
	}
	return fmt.Errorf("unknown STELLA_SANDBOX_BACKEND %q", ActiveSandboxBackend())
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
