package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/internal/plugin"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	nonebackend "github.com/CherryHQ/stella/plugins/sandbox/none"
)

func TestPreparationAcceptanceKnownNativeInstallRetainsBackingAndReusesSelection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture installer uses a POSIX shell")
	}
	db, sessionID, owner, _ := acceptanceGenerationDB(t)
	store := NewGenerationStore(db, owner, nil)
	main, err := store.Open(t.Context(), GenerationSpec{
		SessionID: sessionID, Backend: "none", ConfigDigest: "policy",
		Create: func(context.Context, int64, string) (pkgsandbox.Session, error) { return pkgsandbox.NopSession(), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = main.Close() })
	root := t.TempDir()
	workspace, userRoot := filepath.Join(root, "agents", "agent"), filepath.Join(root, "users", "user")
	for _, dir := range []string{filepath.Join(root, "bin"), workspace, userRoot, filepath.Join(userRoot, "agents", "agent"), filepath.Join(userRoot, "data")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	installer := `#!/bin/sh
set -eu
case "$1" in
trust|reshim) exit 0;;
install)
  mkdir -p "$MISE_DATA_DIR/installs/tool/1/bin"
  printf '#!/bin/sh\necho installed\n' > "$MISE_DATA_DIR/installs/tool/1/bin/tool"
  chmod 755 "$MISE_DATA_DIR/installs/tool/1/bin/tool";;
where) printf '%s\n' "$MISE_DATA_DIR/installs/tool/1";;
which) printf '%s\n' "$MISE_DATA_DIR/installs/tool/1/bin/tool";;
*) exit 9;;
esac
`
	if err := os.WriteFile(filepath.Join(root, "bin", "mise"), []byte(installer), 0o755); err != nil {
		t.Fatal(err)
	}
	var creates atomic.Int32
	var backing string
	backends, err := NewBackendRegistry(BackendDefinition{
		Name: "none", Create: func(ctx context.Context, req BackendRequest) (pkgsandbox.Session, error) {
			creates.Add(1)
			if req.Generation != 1 || req.ExecutorBootID != owner {
				t.Fatalf("preparation lost durable owner labels: generation=%d boot=%s", req.Generation, req.ExecutorBootID)
			}
			for _, source := range req.MountSources {
				if strings.Contains(source, string(filepath.Separator)+".mise-managed"+string(filepath.Separator)) {
					backing = source
				}
			}
			return nonebackend.NewFactoryWithMountSources(req.MountSources, nonebackend.Config{StellaHome: root}).CreateSession(ctx, req.Policy)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	spec := pkgplugins.PluginBinarySpec{
		PluginResourceIdentity: pkgplugins.PluginResourceIdentity{PluginID: "tool", ConfigID: "user-tool", Scope: string(plugin.ScopeUser), Revision: 1},
		Name:                   "tool", Tool: "github:test/tool", Version: "1",
	}
	cfg := Config{
		SessionID: sessionID, UserID: "user", AgentID: "agent", GenerationStore: store,
		Paths:    Paths{StellaHome: root, AgentRoot: workspace, UserRoot: userRoot},
		Backends: backends, BinarySpecs: []pkgplugins.PluginBinarySpec{spec},
	}
	first, err := prepareUserBinarySelection(t.Context(), cfg, config.SandboxBackendNone)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Plan.Selections) != 1 || backing == "" {
		t.Fatalf("installation did not publish a selection: %+v, backing=%s", first, backing)
	}
	if _, err := os.Stat(filepath.Join(backing, "installs")); err != nil {
		t.Fatalf("native preparation backing removed without absence proof: %v", err)
	}
	if _, err := os.Stat(filepath.Join(first.Plan.Selections[0].PublicDir, "tool")); err != nil {
		t.Fatalf("stable public selection unavailable: %v", err)
	}
	row, err := store.Inspect(t.Context(), sessionID)
	if err != nil || row.State != GenerationActive {
		t.Fatalf("known installation result fenced healthy generation: %+v, %v", row, err)
	}
	// A changed system selection must not reinstall an unchanged user package
	// or reuse the still-owned private backing as a new staging tree.
	cfg.ContextBinaryPlan = &BinaryInstallPlan{Identity: "different-system-selection"}
	second, err := prepareUserBinarySelection(t.Context(), cfg, config.SandboxBackendNone)
	if err != nil || len(second.Plan.Selections) != 1 || creates.Load() != 1 {
		t.Fatalf("stable selection was not reused: %+v, creates=%d, err=%v", second, creates.Load(), err)
	}
}
