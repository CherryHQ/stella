package boxsh

import (
	"strings"
	"testing"
	"time"

	"github.com/vaayne/anna/plugins/sandbox/boxsh/boxshclient"
)

func TestBoxshSessionDoneClosesWhenBackendDies(t *testing.T) {
	session := &boxshSession{
		backend: &boxshclient.SharedBackend{}, // Alive() == false because no client is attached
		done:    make(chan struct{}),
	}

	go session.watchBackend()

	select {
	case <-session.Done():
		// expected
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Done() should close when backend is no longer alive")
	}
}

func TestBoxshHostEnsureWritableBlocksReadOnlySubdirUnderWorkspaceRoot(t *testing.T) {
	host := &boxshHost{session: &boxshSession{policy: Policy{Filesystem: FilesystemPolicy{
		WorkspaceRoot: "/repo",
		WorkingDir:    "/repo",
		ReadOnlyPaths: []string{"/repo/docs"},
	}}}}

	err := host.ensureWritable("/repo/docs/file.md")
	if err == nil {
		t.Fatal("expected readonly nested path to be rejected")
	}
	if !strings.Contains(err.Error(), "fail-closed") && !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("expected readonly/fail-closed error, got %v", err)
	}
}
