//go:build system

package system

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"
)

// tool_smoke_canary proves the transport, not the tool surface. Coverage of the
// builtin tools lives in process, in cmd/stellad's TestToolSmoke, which runs the
// same production registry against a live database without paying for a
// subprocess per case; it is closed by strict equality with three documented
// protocol exceptions, and it — not this journey — is where a new tool gets its
// case. What only this layer can show is the rest of the path:
// a real HTTP request, cookie authentication, a Code Mode call whose catalog was
// assembled by the daemon at startup, and each child tool's own result arriving
// on the SSE stream as its own frames.
//
// It calls three tools deliberately: a sandbox core tool, a database-backed
// generated tool, and one whose result the caller must read back, so a
// regression in any of those three shapes is visible here.
func (h *harness) testToolSmokeCanary(t *testing.T) {
	fake := newFakeAnthropic(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	const modelID = "claude-sonnet-4-6"
	providerID := h.createFakeProviderNamed(t, ctx, fake.baseURL(), "anthropic-tool-canary-"+h.runID)
	agentID := h.createAgentNamed(t, ctx, providerID+"/"+modelID, "tool-canary-agent-"+h.runID)
	sessionID := h.createSession(t, ctx, agentID)

	// One VM call, three tools: bash writes a marker, vault_secret_set commits a
	// user-scoped secret, and vault_secret_list reads it back. Chaining them in
	// one call is what makes the SSE assertion meaningful — three child tools
	// must each report their own settled result on the stream.
	const script = `{"code":"const shell = await tools.invoke(\"bash\", { command: \"echo tool-canary\" });\nawait tools.invoke(\"vault_secret_set\", { name: \"TOOL_CANARY\", value: \"tool-smoke-not-a-secret\" });\nconst listed = await tools.invoke(\"vault_secret_list\", {});\nreturn { shell: tools.text(shell), listed: tools.text(listed) };"}`
	fake.enqueueTool("toolu_canary", "code", script)
	fake.enqueueText("canary done")

	events, _ := h.streamChatParts(t, ctx, agentID, sessionID, []map[string]any{
		{"type": "text", "text": "run the tool canary"},
	})

	settled := childToolResults(events)
	for _, tool := range []string{"bash", "vault_secret_set", "vault_secret_list", "code"} {
		result, ok := settled[tool]
		if !ok {
			t.Fatalf("no SSE result for %q; the code call never reached it. frames: %v\n%s",
				tool, eventTypes(events), h.proc.LogTail(60))
		}
		if result.failed {
			t.Fatalf("%s returned an error result over SSE: %s\n%s", tool, result.text, h.proc.LogTail(60))
		}
	}
	if !strings.Contains(settled["bash"].text, "tool-canary") {
		t.Errorf("bash SSE output = %q, want the echoed marker", settled["bash"].text)
	}
	if !strings.Contains(settled["vault_secret_list"].text, "TOOL_CANARY") {
		t.Errorf("vault_secret_list SSE output = %q, want the secret this turn committed", settled["vault_secret_list"].text)
	}
	// The secret's value must never come back out of the vault, over any transport.
	if strings.Contains(settled["vault_secret_list"].text, "tool-smoke-not-a-secret") {
		t.Error("vault_secret_list echoed the secret value; the list surface must return metadata only")
	}

	// The catalog the daemon assembled at startup is what the model can reach, so
	// the canary also proves it is non-trivially populated and that the entry
	// point is not inside it.
	catalog := h.readCanaryCatalog(t, ctx, fake, agentID, sessionID)
	if len(catalog) < 40 {
		t.Errorf("the model's tool catalog has %d entries: %v; the daemon assembled far fewer tools than it registers", len(catalog), catalog)
	}
	for _, name := range catalog {
		if name == "code" {
			t.Error("`code` is inside its own catalog; the entry point must not be reachable as a child call")
		}
	}
}

// childToolResult is one settled child invocation observed on the SSE stream.
type childToolResult struct {
	text   string
	failed bool
}

// childToolResults pairs each tool-input-start frame, which is the only frame
// carrying the tool name, with the settled frame carrying that call's result.
func childToolResults(events []turnEvent) map[string]childToolResult {
	names := map[string]string{}
	settled := map[string]childToolResult{}
	for _, event := range events {
		switch event.Type {
		case "tool-input-start":
			names[event.ToolCallID] = event.ToolName
		case "tool-output-available":
			if name, ok := names[event.ToolCallID]; ok {
				settled[name] = childToolResult{text: event.Output}
			}
		case "tool-output-error":
			if name, ok := names[event.ToolCallID]; ok {
				settled[name] = childToolResult{text: event.ErrorText, failed: true}
			}
		}
	}
	return settled
}

// readCanaryCatalog pages the model's tool catalog out through the same
// tools.search the model uses; it has no Go-side accessor by design.
func (h *harness) readCanaryCatalog(t *testing.T, ctx context.Context, fake *fakeAnthropic, agentID, sessionID string) []string {
	t.Helper()
	const listCatalog = `{"code":"const names = []; for (let offset = 0; ; ) { const page = tools.search(\"\", offset); for (const tool of page) { names.push(tool.name); } if (!page.hasMore) { break; } offset = page.nextOffset; } return names;"}`
	fake.enqueueTool("toolu_canary_catalog", "code", listCatalog)
	fake.enqueueText("catalog listed")

	events, _ := h.streamChatParts(t, ctx, agentID, sessionID, []map[string]any{
		{"type": "text", "text": "list the tool catalog"},
	})
	var raw string
	for _, event := range events {
		if event.Type == "tool-output-available" && event.ToolCallID == "toolu_canary_catalog" {
			raw = event.Output
		}
	}
	if raw == "" {
		t.Fatalf("the catalog call produced no output; frames: %v\n%s", eventTypes(events), h.proc.LogTail(60))
	}
	var names []string
	if err := json.Unmarshal([]byte(raw), &names); err != nil {
		t.Fatalf("the catalog is not a JSON array: %v\n%s", err, raw)
	}
	sort.Strings(names)
	t.Logf("code catalog (%d tools): %s", len(names), strings.Join(names, " "))
	return names
}
