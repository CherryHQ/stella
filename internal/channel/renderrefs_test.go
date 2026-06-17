package channel

import (
	"testing"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/renderrefs"
)

func TestConvertEventPropagatesReferences(t *testing.T) {
	refs := []renderrefs.Reference{{V: 1, Type: "goal", ID: "goal-1"}}
	out := convertEvent(agent.Event{Text: "done", References: refs})
	if out.Text != "done" {
		t.Fatalf("Text = %q", out.Text)
	}
	if len(out.References) != 1 || out.References[0].ID != "goal-1" {
		t.Fatalf("References = %#v", out.References)
	}
}
