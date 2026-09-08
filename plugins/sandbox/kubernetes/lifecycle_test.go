package kubernetes

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	sandbox "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/plugins/sandbox/internal/sessionfs"
)

func TestCloseRetriesWithoutReleasingRoots(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	tmp := filepath.Join(root, "temp")
	for _, p := range []string{workspace, tmp} {
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	resolver, err := sessionfs.NewResolver("/workspace", []sessionfs.Mount{{HostPath: workspace, SandboxPath: "/workspace"}})
	if err != nil {
		t.Fatal(err)
	}
	pod := &core.Pod{ObjectMeta: meta.ObjectMeta{Name: "owned", Namespace: "test", UID: "uid", Finalizers: []string{finalizer, "other.example/finalizer"}}, Status: core.PodStatus{Phase: core.PodFailed}}
	api := fake.NewClientset(pod)
	unavailable := true
	api.PrependReactor("delete", "pods", func(clienttesting.Action) (bool, runtime.Object, error) {
		if unavailable {
			return true, nil, errors.New("API unavailable")
		}
		return true, nil, nil
	})
	s := &session{client: &Client{api: api, cfg: Config{Namespace: "test"}}, pod: pod, resolver: resolver, files: sessionfs.NewAccess(resolver), tmp: tmp, done: make(chan struct{})}
	resilient := sandbox.NewResilientSession(s, func(_ context.Context) (sandbox.Session, error) {
		t.Fatal("must not recreate after explicit close")
		return nil, nil
	})
	if err = resilient.Close(); err == nil {
		t.Fatal("Close hid API failure")
	}
	if _, err = os.Stat(tmp); err != nil {
		t.Fatal("released temp before termination")
	}
	if !s.Alive() {
		t.Fatal("unconfirmed termination permits recreation")
	}
	if _, err = s.Files().ReadFile("anything"); err == nil {
		t.Fatal("invalid generation remains usable")
	}
	unavailable = false
	if err = resilient.Close(); err != nil {
		t.Fatal(err)
	}
	if s.Alive() {
		t.Fatal("confirmed termination still alive")
	}
	got, err := api.CoreV1().Pods("test").Get(t.Context(), "owned", meta.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Finalizers) != 1 || got.Finalizers[0] != "other.example/finalizer" {
		t.Fatalf("removed foreign finalizer: %v", got.Finalizers)
	}
}

func TestFileViewRejectsRootReplacement(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "workspace")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	resolver, err := sessionfs.NewResolver("/workspace", []sessionfs.Mount{{HostPath: source, SandboxPath: "/workspace"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resolver.Close() }()
	s := &session{resolver: resolver, files: sessionfs.NewAccess(resolver)}
	view := s.Files()
	if err = view.WriteFile("old", []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(source, source+"-old"); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err = view.ReadFile("old"); err == nil {
		t.Fatal("old view survives root replacement")
	}
}
