package agent

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/pkg/tools"
)

type dummyTool struct{ name string }

func (d *dummyTool) Definition() tools.Definition {
	return tools.Definition{Name: d.name}
}

func (d *dummyTool) Execute(_ context.Context, _ map[string]any) (string, error) {
	return "ok", nil
}

func TestMergeTools(t *testing.T) {
	core := []tools.Tool{&dummyTool{name: "bash"}, &dummyTool{name: "read"}}
	plugin := []tools.Tool{&dummyTool{name: "webfetch"}}

	merged := mergeTools(core, plugin)
	if len(merged) != 3 {
		t.Errorf("expected 3 merged tools, got %d", len(merged))
	}
	if merged[0].Definition().Name != "bash" {
		t.Errorf("expected first tool 'bash', got %q", merged[0].Definition().Name)
	}
	if merged[2].Definition().Name != "webfetch" {
		t.Errorf("expected last tool 'webfetch', got %q", merged[2].Definition().Name)
	}
}

func TestMergeTools_Empty(t *testing.T) {
	merged := mergeTools(nil, nil)
	if len(merged) != 0 {
		t.Errorf("expected empty merged tools, got %d", len(merged))
	}
}
