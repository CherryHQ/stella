package kubernetes

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	s := &session{client: &Client{api: api, owner: &core.Pod{ObjectMeta: meta.ObjectMeta{Namespace: "test"}}}, pod: pod, resolver: resolver, files: sessionfs.NewAccess(resolver), tmp: tmp, done: make(chan struct{})}
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

func TestCreateSessionMountsCoreSelectionFromPVC(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	workspace := filepath.Join(home, "workspace")
	coreDir := filepath.Join(home, "core")
	for _, dir := range []string{workspace, coreDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	api := fake.NewClientset()
	var captured *core.Pod
	api.PrependReactor("create", "pods", func(action clienttesting.Action) (bool, runtime.Object, error) {
		create := action.(clienttesting.CreateAction)
		captured = create.GetObject().(*core.Pod).DeepCopy()
		return true, nil, apierrors.NewForbidden(core.Resource("pods"), captured.Name, errors.New("test boundary"))
	})
	c := &Client{
		api:          api,
		cfg:          Config{Image: "sandbox:test", StellaHome: home, BundleRevision: "bundle"},
		owner:        &core.Pod{ObjectMeta: meta.ObjectMeta{Name: "server", Namespace: "tenant", UID: "owner"}},
		pvc:          &core.PersistentVolumeClaim{ObjectMeta: meta.ObjectMeta{Name: "home", Namespace: "tenant", UID: "pvc"}},
		volumePrefix: ".",
	}
	policy := sandbox.Policy{Filesystem: sandbox.FilesystemPolicy{
		WorkingDir: "/workspace",
		Mounts: []sandbox.Mount{
			{SandboxPath: "/workspace", Access: sandbox.MountReadWrite},
			{SandboxPath: "/opt/stella/bin", Access: sandbox.MountReadOnly},
		},
	}}
	_, err := c.Factory(map[string]string{"/workspace": workspace, "/opt/stella/bin": coreDir}).CreateSession(t.Context(), policy)
	if err == nil || captured == nil {
		t.Fatalf("CreateSession err=%v captured=%v", err, captured != nil)
	}
	var got *core.VolumeMount
	for _, mount := range captured.Spec.Containers[0].VolumeMounts {
		if mount.MountPath == "/opt/stella/bin" {
			got = &mount
			break
		}
	}
	if got == nil {
		t.Fatal("core selection PVC mount missing")
	}
	if got.Name != "home" || got.SubPath != "core" || !got.ReadOnly {
		t.Fatalf("core selection mount = %+v", *got)
	}
}
