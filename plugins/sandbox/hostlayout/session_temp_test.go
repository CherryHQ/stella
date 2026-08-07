package hostlayout

import (
	"strings"
	"testing"
)

func TestCreateSessionTempDirRejectsAuthorizedCacheOverlap(t *testing.T) {
	workspace := t.TempDir()
	layout := Layout{WorkspaceSource: workspace, WorkingDirSource: workspace, Mounts: []Mount{{Source: workspace, Target: "/workspace", Access: ReadWrite}}}

	_, err := createSessionTempDir(layout, workspace, "test-*")
	if err == nil || !strings.Contains(err.Error(), "overlaps an authorized layout source") {
		t.Fatalf("CreateSessionTempDir overlap error = %v", err)
	}
}
