package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/vision"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	noneplugin "github.com/CherryHQ/stella/plugins/sandbox/none"
)

func newTestVisionSession(t *testing.T, projectRoot string) pkgsandbox.Session {
	t.Helper()
	policy := pkgsandbox.Policy{
		Filesystem: pkgsandbox.FilesystemPolicy{
			WorkingDir: pkgsandbox.MountWorkspace,
			Mounts:     []pkgsandbox.Mount{{SandboxPath: pkgsandbox.MountWorkspace, Access: pkgsandbox.MountReadWrite}},
		},
		Network: pkgsandbox.NetworkPolicy{Mode: pkgsandbox.NetworkAllowAll},
	}
	session, err := noneplugin.NewFactoryWithMountSources(map[string]string{pkgsandbox.MountWorkspace: projectRoot}, noneplugin.Config{}).CreateSession(context.Background(), policy)
	if err != nil {
		t.Fatalf("create test sandbox Session: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestNewToolsHidesVLLMWithoutVisionModel(t *testing.T) {
	host := newTestVisionSession(t, t.TempDir())

	for name, svc := range map[string]*vision.Service{
		"nil service":       nil,
		"no model resolved": vision.New(vision.Options{}),
	} {
		t.Run(name, func(t *testing.T) {
			for _, tool := range NewTools(host, nil, svc) {
				if tool.Definition().Name == "vllm" {
					t.Fatal("vllm must stay hidden when no vision model is configured")
				}
			}
		})
	}
}

// A text file handed to vllm must fail here with actionable wording rather than
// travelling to the provider as a malformed image payload.
func TestVLLMRejectsNonImage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("plain text, not an image"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	host := newTestVisionSession(t, dir)

	tool := newVLLMTool(host, vision.New(vision.Options{}))
	_, err := tool.Execute(context.Background(), map[string]any{"path": "notes.txt"})
	if err == nil {
		t.Fatal("expected an error for a non-image path")
	}
	if !strings.Contains(err.Error(), "not a recognized image") {
		t.Fatalf("error should name the cause, got: %v", err)
	}
	if !strings.Contains(err.Error(), "xberg extract") {
		t.Fatalf("error should point at the tool that does handle text, got: %v", err)
	}
}
