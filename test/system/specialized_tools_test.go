//go:build system

package system

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/test/testbed/mcpfixture"
)

// testSpecializedToolsFreshTestbed exercises the same real subprocess and HTTP
// boundary as the Harbor specialized lane. The tool list is an admission input,
// not a mocked projection: the following turn attests the exact surface the
// runner passed to the provider.
func (h *harness) testSpecializedToolsFreshTestbed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	fake := newFakeAnthropic(t)
	fake.enqueueText("specialized tools surface admitted")
	providerID := h.createFakeProviderNamed(t, ctx, fake.baseURL(), "specialized-tools-"+h.runID)
	agentID := h.createAgentNamed(t, ctx, providerID+"/claude-sonnet-4-6", "specialized-tools-"+h.runID)

	tools := h.listAgentTools(t, ctx, agentID)
	assertFrozenBuiltinUnion(t, tools, false)
	for _, tool := range tools {
		if tool.Source != "builtin" {
			continue
		}
		enabled := tool.Name == "skills" || tool.Name == "memory" || tool.Name == "library_search" || tool.Name == "recally"
		h.updateAgentTool(t, ctx, agentID, tool.Name, enabled)
	}
	assertFrozenBuiltinUnion(t, h.listAgentTools(t, ctx, agentID), true)
	registrationID := h.registerTestbedMCPFixture(t, ctx, agentID)

	// This fixture reproduces the cleanup precondition. library_file deliberately
	// RESTRICTs Agent deletion, so successful cleanup must remove this through
	// the Library API before DELETE /api/agents/{id}.
	h.addMemoryFixture(t, ctx, agentID)
	libraryID := h.uploadLibraryFixture(t, ctx, agentID)

	sessionID := h.createSession(t, ctx, agentID)
	_, reply := h.streamChatPartsWithExcludedTools(t, ctx, agentID, sessionID,
		[]map[string]any{{"type": "text", "text": "attest the native tool surface"}}, []string{"view_image", "vllm"})
	if reply != "specialized tools surface admitted" {
		t.Fatalf("specialized surface reply = %q", reply)
	}
	assertFrozenRuntimeUnion(t, h.runtimeToolSurface(t, ctx, agentID, sessionID))

	h.deletePath(t, ctx, "/api/mcp/servers/"+registrationID+"?scope=user_agent&agent_id="+url.QueryEscape(agentID), http.StatusNoContent)
	h.deleteTrialLibraryFixtures(t, ctx, agentID, libraryID)
	h.deleteAgentAfterLibraryFixture(t, ctx, agentID)
	var remaining int
	if err := h.db.QueryRow(ctx, "SELECT count(*) FROM library_file WHERE agent_id = $1", agentID).Scan(&remaining); err != nil {
		t.Fatalf("count cleaned Library files: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("Library files remaining after normal cleanup = %d, want 0", remaining)
	}
}

type liveAgentTool struct {
	Name    string `json:"name"`
	Source  string `json:"source"`
	Enabled bool   `json:"enabled"`
}

func (h *harness) listAgentTools(t *testing.T, ctx context.Context, agentID string) []liveAgentTool {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.baseURL+"/api/agents/"+agentID+"/tools", nil)
	if err != nil {
		t.Fatalf("build tool inventory request: %v", err)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("GET agent tools: %v\n%s", err, h.proc.logTail(40))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET agent tools = %d, want %d\n%s", resp.StatusCode, http.StatusOK, h.proc.logTail(40))
	}
	var out struct {
		Tools []liveAgentTool `json:"tools"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode agent tools: %v", err)
	}
	return out.Tools
}

func (h *harness) updateAgentTool(t *testing.T, ctx context.Context, agentID, tool string, enabled bool) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"enabled": enabled, "scope": "user_agent"})
	if err != nil {
		t.Fatalf("marshal tool policy: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, fmt.Sprintf("%s/api/agents/%s/tools/%s", h.baseURL, agentID, tool), bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build tool policy request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("PATCH tool policy: %v\n%s", err, h.proc.logTail(40))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH tool %s = %d, want %d\n%s", tool, resp.StatusCode, http.StatusOK, h.proc.logTail(40))
	}
}

func assertFrozenBuiltinUnion(t *testing.T, tools []liveAgentTool, requireExclusions bool) {
	t.Helper()
	want := map[string]string{"bash": "core", "skills": "builtin", "memory": "builtin", "library_search": "builtin", "recally": "builtin"}
	seen := make(map[string]liveAgentTool, len(tools))
	for _, tool := range tools {
		seen[tool.Name] = tool
	}
	for name, source := range want {
		tool, ok := seen[name]
		if !ok || tool.Source != source || !tool.Enabled {
			t.Fatalf("live tool inventory missing admitted %s tool %q: %#v", source, name, tool)
		}
	}
	if requireExclusions {
		for _, tool := range tools {
			if tool.Source == "builtin" && tool.Enabled && want[tool.Name] == "" {
				t.Fatalf("non-lane builtin %q remains enabled", tool.Name)
			}
		}
	}
}

func (h *harness) addMemoryFixture(t *testing.T, ctx context.Context, agentID string) {
	t.Helper()
	resp := h.postJSON(t, ctx, "/api/users/me/memories/"+agentID+"/knowledge", map[string]string{"content": "cleanup fixture"})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST memory fixture = %d, want %d\n%s", resp.StatusCode, http.StatusCreated, h.proc.logTail(40))
	}
}

func (h *harness) uploadLibraryFixture(t *testing.T, ctx context.Context, agentID string) string {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "cleanup.txt")
	if err != nil {
		t.Fatalf("create Library form: %v", err)
	}
	if _, err := part.Write([]byte("cleanup fixture\n")); err != nil {
		t.Fatalf("write Library form: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close Library form: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+"/api/library-files?scope=user_agent&agent_id="+agentID, &body)
	if err != nil {
		t.Fatalf("build Library upload: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("POST Library fixture: %v\n%s", err, h.proc.logTail(40))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST Library fixture = %d, want %d\n%s", resp.StatusCode, http.StatusCreated, h.proc.logTail(40))
	}
	var file struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&file); err != nil || file.ID == "" {
		t.Fatalf("decode Library fixture: id=%q err=%v", file.ID, err)
	}
	return file.ID
}

func (h *harness) runtimeToolSurface(t *testing.T, ctx context.Context, agentID, sessionID string) []string {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/api/agents/%s/sessions/%s", h.baseURL, agentID, sessionID), nil)
	if err != nil {
		t.Fatalf("build session detail request: %v", err)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("GET session detail: %v\n%s", err, h.proc.logTail(40))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET session detail = %d, want %d\n%s", resp.StatusCode, http.StatusOK, h.proc.logTail(40))
	}
	var detail struct {
		ToolSurface *struct {
			Strategy string `json:"strategy"`
			Tools    []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"tool_surface"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		t.Fatalf("decode session detail: %v", err)
	}
	if detail.ToolSurface == nil || detail.ToolSurface.Strategy != "native" {
		t.Fatalf("runtime tool surface = %#v, want native", detail.ToolSurface)
	}
	names := make([]string, 0, len(detail.ToolSurface.Tools))
	for _, tool := range detail.ToolSurface.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	return names
}

func (h *harness) registerTestbedMCPFixture(t *testing.T, ctx context.Context, agentID string) string {
	t.Helper()
	resp := h.postJSON(t, ctx, "/api/mcp/servers", map[string]any{
		"name": mcpfixture.RegistrationName, "url": "http://" + h.mcpFixtureAuthority + "/mcp",
		"scope": "user_agent", "agent_id": agentID, "transport": "streamable_http", "auth_type": "none",
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST testbed MCP registration = %d, want %d\n%s", resp.StatusCode, http.StatusCreated, h.proc.logTail(40))
	}
	var registration struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&registration); err != nil || registration.ID == "" {
		t.Fatalf("decode testbed MCP registration: id=%q err=%v", registration.ID, err)
	}
	return registration.ID
}

func assertFrozenRuntimeUnion(t *testing.T, names []string) {
	t.Helper()
	want := append([]string{"bash", "library_search", "memory", "recally", "skills"}, mcpfixture.NamespacedTools()...)
	sort.Strings(want)
	wantDigest := canonicalToolNameDigest(want)
	gotDigest := canonicalToolNameDigest(names)
	if len(names) != len(want) || gotDigest != wantDigest {
		t.Fatalf("runtime surface count=%d digest=%s, want count=%d digest=%s", len(names), gotDigest, len(want), wantDigest)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("runtime surface count=%d digest=%s does not match the canonical testbed catalog", len(names), gotDigest)
		}
	}
}

func canonicalToolNameDigest(names []string) string {
	canonical := append([]string(nil), names...)
	sort.Strings(canonical)
	sum := sha256.Sum256([]byte(strings.Join(canonical, "\n")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (h *harness) deleteTrialLibraryFixtures(t *testing.T, ctx context.Context, agentID, wantID string) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.baseURL+"/api/library-files?scope=user_agent&agent_id="+agentID, nil)
	if err != nil {
		t.Fatalf("build Library cleanup list: %v", err)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("GET Library cleanup list: %v\n%s", err, h.proc.logTail(40))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET Library cleanup list = %d, want %d\n%s", resp.StatusCode, http.StatusOK, h.proc.logTail(40))
	}
	var list struct {
		LibraryFiles []struct {
			ID string `json:"id"`
		} `json:"library_files"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decode Library cleanup list: %v", err)
	}
	if len(list.LibraryFiles) != 1 || list.LibraryFiles[0].ID != wantID {
		t.Fatalf("Library cleanup list = %+v, want sole fixture %q", list.LibraryFiles, wantID)
	}
	h.deletePath(t, ctx, "/api/library-files/"+list.LibraryFiles[0].ID, http.StatusNoContent)
}

func (h *harness) deleteAgentAfterLibraryFixture(t *testing.T, ctx context.Context, id string) {
	t.Helper()
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, h.baseURL+"/api/agents/"+id, nil)
		if err != nil {
			t.Fatalf("build Agent cleanup request: %v", err)
		}
		resp, err := h.client.Do(req)
		if err != nil {
			t.Fatalf("DELETE Agent cleanup: %v\n%s", err, h.proc.logTail(40))
		}
		status := resp.StatusCode
		_ = resp.Body.Close()
		if status == http.StatusNoContent {
			return
		}
		if status != http.StatusInternalServerError {
			t.Fatalf("DELETE Agent cleanup = %d, want %d\n%s", status, http.StatusNoContent, h.proc.logTail(40))
		}
		select {
		case <-ctx.Done():
			t.Fatalf("DELETE Agent cleanup never completed: %v\n%s", ctx.Err(), h.proc.logTail(40))
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (h *harness) deletePath(t *testing.T, ctx context.Context, path string, want int) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, h.baseURL+path, nil)
	if err != nil {
		t.Fatalf("build DELETE %s: %v", path, err)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("DELETE %s: %v\n%s", path, err, h.proc.logTail(40))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != want {
		t.Fatalf("DELETE %s = %d, want %d\n%s", path, resp.StatusCode, want, h.proc.logTail(40))
	}
}
