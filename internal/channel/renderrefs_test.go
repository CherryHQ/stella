package channel

import (
	"testing"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/pkg/renderrefs"
)

func TestConvertEventPropagatesReferences(t *testing.T) {
	refs := []renderrefs.Reference{{V: 1, Type: "goal", ID: "goal-1"}}
	// The runner sets references only on the tool event; the coordinator fans them
	// out to the event-level field for channel consumers like Feishu.
	out := convertEvent(agent.Event{
		Text:    "done",
		ToolUse: &agent.ToolUseEvent{ID: "call-1", References: refs},
	})
	if out.Text != "done" {
		t.Fatalf("Text = %q", out.Text)
	}
	if len(out.References) != 1 || out.References[0].ID != "goal-1" {
		t.Fatalf("References = %#v", out.References)
	}
	if out.ToolUse == nil || len(out.ToolUse.References) != 1 || out.ToolUse.References[0].ID != "goal-1" {
		t.Fatalf("ToolUse.References = %#v", out.ToolUse)
	}
}
