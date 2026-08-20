// Package bridge is an evaluation-only sandbox backend. It executes every
// command and file operation through a per-trial bridge socket owned by an
// external harness (the Harbor adapter), which forwards them into the
// benchmark task container. Nothing runs on the stellad host.
//
// The backend never falls back: a session whose principal has no binding, or
// whose bridge socket is unreachable, fails instead of running elsewhere. That
// is the property an evaluation depends on — a silent fallback would grade the
// wrong environment while looking like a pass.
package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BindingDirEnv names the directory where the harness publishes one binding
// file per provisioned eval user: <dir>/<user_id>.json.
const BindingDirEnv = "STELLA_EVAL_BRIDGE_DIR"

// Binding is what the harness writes for one trial. Socket and Nonce identify
// the bridge; the rest describes the task container as the harness observed it
// at bind time so the session env mirrors the container's own view.
type Binding struct {
	Socket string `json:"socket"`
	Nonce  string `json:"nonce"`
	// WorkDir is the task working directory inside the container (e.g. /app).
	WorkDir string `json:"workdir"`
	// Home is the container user's HOME. Empty falls back to WorkDir.
	Home string `json:"home,omitempty"`
	// TempDir is a per-trial scratch directory the harness created in the
	// container. Empty falls back to /tmp.
	TempDir string `json:"temp_dir,omitempty"`
	// Path is the container PATH the harness discovered, already prefixed with
	// the Stella helper tool bundle. Empty leaves PATH untouched.
	Path string `json:"path,omitempty"`
}

// ErrNoBinding reports that the principal has no published bridge binding.
var ErrNoBinding = errors.New("bridge: no binding for principal; refusing to create a session")

// LoadBinding reads and validates the binding for userID from dir.
func LoadBinding(dir, userID string) (Binding, error) {
	if strings.TrimSpace(dir) == "" {
		return Binding{}, fmt.Errorf("bridge: %s is not set", BindingDirEnv)
	}
	if userID == "" || strings.ContainsAny(userID, `/\`) || userID == "." || userID == ".." {
		return Binding{}, fmt.Errorf("bridge: invalid principal id %q", userID)
	}
	data, err := os.ReadFile(filepath.Join(dir, userID+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return Binding{}, ErrNoBinding
	}
	if err != nil {
		return Binding{}, fmt.Errorf("bridge: read binding: %w", err)
	}
	var b Binding
	if err := json.Unmarshal(data, &b); err != nil {
		return Binding{}, fmt.Errorf("bridge: parse binding: %w", err)
	}
	if err := b.validate(); err != nil {
		return Binding{}, err
	}
	return b, nil
}

func (b Binding) validate() error {
	switch {
	case b.Socket == "":
		return errors.New("bridge: binding requires socket")
	case b.Nonce == "":
		return errors.New("bridge: binding requires nonce")
	case !strings.HasPrefix(b.WorkDir, "/"):
		return fmt.Errorf("bridge: binding workdir must be absolute, got %q", b.WorkDir)
	}
	return nil
}
