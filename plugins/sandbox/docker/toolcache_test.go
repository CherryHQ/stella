package docker

import (
	"strings"
	"testing"
)

func TestUserToolsMiseTOMLRegistryTool(t *testing.T) {
	got, err := userToolsMiseTOML([]ToolBinary{{Name: "uv", Tool: "uv"}})
	if err != nil {
		t.Fatalf("userToolsMiseTOML: %v", err)
	}
	want := `uv = 'latest'`
	if !strings.Contains(got, want) {
		t.Fatalf("expected registry tool form %q in:\n%s", want, got)
	}
}
