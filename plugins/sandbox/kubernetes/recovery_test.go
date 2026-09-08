package kubernetes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	sandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// The host testbed replaces its own owner Pod between these two invocations.
// Prepare deliberately exits without Close, reproducing a lost controller.
func TestOwnerRecovery(t *testing.T) {
	phase := os.Getenv("STELLA_KUBERNETES_RECOVERY")
	if phase == "" {
		t.Skip("owned by test:kubernetes")
	}
	home := "/data/owner-recovery"
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	bundle, err := os.Readlink("/opt/stella/skills/builtin")
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Namespace: os.Getenv("STELLA_KUBERNETES_NAMESPACE"), OwnerName: os.Getenv("STELLA_KUBERNETES_POD_NAME"), OwnerUID: types.UID(os.Getenv("STELLA_KUBERNETES_POD_UID")), NodeName: os.Getenv("STELLA_KUBERNETES_NODE_NAME"), Deployment: "testbed", PVC: "home", Image: os.Getenv("STELLA_KUBERNETES_IMAGE"), ServerURL: os.Getenv("STELLA_SANDBOX_SERVER_URL"), StellaHome: home, BundleRevision: strings.TrimPrefix(bundle, "../bundles/")}
	c, err := NewInCluster(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	policy := sandbox.Policy{Filesystem: sandbox.FilesystemPolicy{WorkingDir: "/workspace", Mounts: []sandbox.Mount{{SandboxPath: "/workspace", Access: sandbox.MountReadWrite}}}}
	s, err := c.Factory(map[string]string{"/workspace": workspace}).CreateSession(t.Context(), policy)
	if err != nil {
		t.Fatal(err)
	}
	if phase == "prepare" {
		if err = s.Files().WriteFile("persistent", []byte("survived-owner-replacement"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(home, "old-owner"), []byte(cfg.OwnerUID), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	if phase != "recover" {
		t.Fatal("unknown recovery phase")
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	old, err := os.ReadFile(filepath.Join(home, "old-owner"))
	if err != nil {
		t.Fatal(err)
	}
	if string(old) == string(cfg.OwnerUID) {
		t.Fatal("owner Pod was not replaced")
	}
	pods, err := c.api.CoreV1().Pods(cfg.Namespace).List(t.Context(), meta.ListOptions{LabelSelector: labelDeployment + "=testbed"})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pods.Items {
		if p.Labels[labelBoot] != c.boot {
			t.Fatalf("old execution remains: %s", p.Name)
		}
	}
	result, err := s.Exec(t.Context(), "cat persistent", sandbox.ExecOptions{})
	if err != nil || result.Stdout != "survived-owner-replacement" {
		t.Fatalf("persistent bytes: %+v %v", result, err)
	}
}
