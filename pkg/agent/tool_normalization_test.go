package agent

import (
	"strings"
	"testing"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/renderrefs"
)

func TestNormalizeToolResultPreservesTextSignatureAndReferenceIdentity(t *testing.T) {
	first := renderrefs.Reference{V: 1, Type: "task", ID: "same", Preview: &renderrefs.Preview{Title: "first"}}
	second := renderrefs.Reference{V: 1, Type: "task", ID: "same", Preview: &renderrefs.Preview{Title: "second"}}
	var sentinel strings.Builder
	if err := renderrefs.Emit(&sentinel, second); err != nil {
		t.Fatal(err)
	}
	result := NormalizeToolResult(ai.ToolResultMessage{
		Content:    []ai.ContentBlock{ai.TextContent{Text: "done\n" + sentinel.String(), TextSignature: "signature"}},
		References: []renderrefs.Reference{first},
	})
	text := result.Content[0].(ai.TextContent)
	if text.TextSignature != "signature" || strings.Contains(text.Text, "::stella-ref/") {
		t.Fatalf("normalized text = %#v", text)
	}
	if len(result.References) != 1 || result.References[0].Preview == nil || result.References[0].Preview.Title != "first" {
		t.Fatalf("references = %#v, want first identity match", result.References)
	}
}
