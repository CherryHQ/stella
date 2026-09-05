//go:build system

package system

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"slices"
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
	agentID := h.createAgentNamedWithSettingsTools(t, ctx, providerID+"/"+modelID, "tool-canary-agent-"+h.runID, true)
	sessionID := h.createSession(t, ctx, agentID)

	// One VM call crosses four distinct seams: bash writes a binary ZIP into the
	// sandbox, the managed-Skill tool reads it through FileAccess, the database
	// commits the complete package, and the vault pair proves unrelated generated
	// tools still share the same Code Mode invocation.
	zipCommand := "printf '%b' '" + toolCanarySkillZIP(t) + "' > tool-canary-skill.zip && echo tool-canary"
	source := fmt.Sprintf(`const shell = await tools.invoke("bash", { command: %q });
await tools.invoke("vault_secret_set", { name: "TOOL_CANARY", value: "tool-smoke-not-a-secret" });
const listed = await tools.invoke("vault_secret_list", {});
const created = await tools.invoke("settings_skill_create", { scope: "user", content_path: "tool-canary-skill.zip" });
return { shell: tools.text(shell), listed: tools.text(listed), created: tools.text(created) };`, zipCommand)
	fake.enqueueTool("toolu_canary", "code", codeToolArguments(t, source))
	fake.enqueueText("canary done")

	events, _ := h.streamChatParts(t, ctx, agentID, sessionID, []map[string]any{
		{"type": "text", "text": "run the tool canary"},
	})

	settled := childToolResults(events)
	for _, tool := range []string{"bash", "vault_secret_set", "vault_secret_list", "settings_skill_create", "code"} {
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

	var created struct {
		ID      string   `json:"id"`
		Files   []string `json:"files"`
		Version string   `json:"version"`
	}
	if err := json.Unmarshal([]byte(settled["settings_skill_create"].text), &created); err != nil {
		t.Fatalf("decode settings_skill_create result: %v\n%s", err, settled["settings_skill_create"].text)
	}
	wantFiles := []string{"SKILL.md", "assets/font.woff2", "references/check.md"}
	if created.ID == "" || created.Version == "" || !slices.Equal(created.Files, wantFiles) {
		t.Fatalf("settings_skill_create = %#v, want non-empty identity and files %v", created, wantFiles)
	}

	// A second real agent turn reads the committed package and removes it again.
	// Passing the returned version proves the model-facing optimistic-lock token
	// survives the create/get/delete tool boundary intact.
	source = fmt.Sprintf(`const got = await tools.invoke("settings_skill_get", { id: %q });
const deleted = await tools.invoke("settings_skill_delete", { id: %q, expected_version: %q });
return { got: tools.text(got), deleted: tools.text(deleted) };`, created.ID, created.ID, created.Version)
	fake.enqueueTool("toolu_canary_skill_read_delete", "code", codeToolArguments(t, source))
	fake.enqueueText("skill checked and removed")
	cleanupEvents, _ := h.streamChatParts(t, ctx, agentID, sessionID, []map[string]any{
		{"type": "text", "text": "verify and remove the ZIP Skill"},
	})
	cleanupSettled := childToolResults(cleanupEvents)
	for _, tool := range []string{"settings_skill_get", "settings_skill_delete", "code"} {
		result, ok := cleanupSettled[tool]
		if !ok {
			t.Fatalf("no SSE result for %q; frames: %v\n%s", tool, eventTypes(cleanupEvents), h.proc.LogTail(60))
		}
		if result.failed {
			t.Fatalf("%s returned an error result over SSE: %s\n%s", tool, result.text, h.proc.LogTail(60))
		}
	}
	if !strings.Contains(cleanupSettled["settings_skill_get"].text, `"assets/font.woff2"`) {
		t.Errorf("settings_skill_get output omitted the binary asset: %s", cleanupSettled["settings_skill_get"].text)
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

func codeToolArguments(t *testing.T, source string) string {
	t.Helper()
	args, err := json.Marshal(map[string]string{"code": source})
	if err != nil {
		t.Fatalf("marshal code tool arguments: %v", err)
	}
	return string(args)
}

// toolCanarySkillZIP uses POSIX printf %b octal escapes. The sandbox runs sh,
// which may be dash on Linux and cannot decode bash-specific hex escapes.
func toolCanarySkillZIP(t *testing.T) string {
	t.Helper()
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	for _, file := range []struct {
		name string
		body []byte
	}{
		{name: "downloaded-package/SKILL.md", body: []byte("---\nname: tool-canary-skill\ndescription: Installed by the system tool canary.\n---\n# Tool canary\n")},
		{name: "downloaded-package/assets/font.woff2", body: []byte{0x77, 0x4f, 0x46, 0x32, 0xff, 0x00}},
		{name: "downloaded-package/references/check.md", body: []byte("ZIP resource works.\n")},
	} {
		entry, err := zw.Create(file.name)
		if err != nil {
			t.Fatalf("create ZIP entry %q: %v", file.name, err)
		}
		if _, err := entry.Write(file.body); err != nil {
			t.Fatalf("write ZIP entry %q: %v", file.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close ZIP: %v", err)
	}

	escaped := make([]byte, 0, archive.Len()*5)
	for _, b := range archive.Bytes() {
		escaped = fmt.Appendf(escaped, `\0%03o`, b)
	}
	return string(escaped)
}

func TestToolCanarySkillZIP(t *testing.T) {
	for _, shell := range []string{"sh", "dash"} {
		t.Run(shell, func(t *testing.T) {
			path, err := exec.LookPath(shell)
			if err != nil {
				t.Skipf("%s is unavailable: %v", shell, err)
			}
			cmd := exec.CommandContext(t.Context(), path, "-c", `printf '%b' "$1"`, "printf", toolCanarySkillZIP(t))
			data, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatalf("%s printf produced an invalid ZIP: %v", shell, err)
			}
			asset, err := archive.Open("downloaded-package/assets/font.woff2")
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(asset)
			if closeErr := asset.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(body, []byte{0x77, 0x4f, 0x46, 0x32, 0xff, 0x00}) {
				t.Fatalf("%s printf corrupted the binary asset: %x", shell, body)
			}
		})
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
