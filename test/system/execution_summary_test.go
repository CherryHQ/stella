//go:build system

package system

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	apitypes "github.com/CherryHQ/stella/api/types"
)

// testExecutionSummarySurvivesPackageUpdateAndRestart proves the black-box
// contract for an execution summary. A real process admits an enabled package,
// executes a text-only turn, and persists the selected package/Skill identity
// beside the user message. The provider receives only ordinary model messages,
// and a later package update plus forced restart cannot rewrite the old record.
func (h *harness) testExecutionSummarySurvivesPackageUpdateAndRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	const (
		modelID     = "claude-sonnet-4-6"
		packageV1   = "1.2.3"
		packageV2   = "9.9.9"
		skillName   = "execution-canary"
		skillMarker = "execution-summary package body must stay out of history"
		userText    = "record the execution summary"
	)

	pluginID := "execution-summary-" + h.runID
	firstSource := executionSummaryPackage(t, pluginID, packageV1, skillName, skillMarker)
	createdResponse := h.postJSON(t, ctx, "/api/plugins/import", map[string]any{
		"source_path": firstSource,
		"initial_config": map[string]any{
			"scope": "system", "is_enabled": true, "config": map[string]any{},
		},
	})
	defer func() { _ = createdResponse.Body.Close() }()
	if createdResponse.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/plugins/import = %d, want %d\n%s", createdResponse.StatusCode, http.StatusCreated, h.proc.LogTail(60))
	}
	var created apitypes.CreatePluginResponse
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatalf("decode imported package: %v", err)
	}
	if created.Plugin.Id != pluginID || created.Plugin.Revision == nil {
		t.Fatalf("imported package = %#v, want id %q with revision", created.Plugin, pluginID)
	}

	fake := newFakeAnthropic(t)
	providerID := h.createFakeProviderNamed(t, ctx, fake.baseURL(), "anthropic-execution-summary-"+h.runID)
	agentID := h.createAgentNamedWithSettingsTools(t, ctx, providerID+"/"+modelID, "execution-summary-agent-"+h.runID, true)
	sessionID := h.createSession(t, ctx, agentID)
	fake.enqueueText("execution summary recorded")

	events, reply := h.streamChatTurn(t, ctx, agentID, sessionID, userText)
	if reply != "execution summary recorded" {
		t.Fatalf("assistant reply = %q, want scripted response; frames=%v\n%s", reply, eventTypes(events), h.proc.LogTail(60))
	}

	// A second text-only turn forces the first turn's persisted execution
	// metadata through history assembly. It must remain provider-invisible.
	fake.enqueueText("second execution summary recorded")
	secondEvents, secondReply := h.streamChatTurn(t, ctx, agentID, sessionID, "read the prior result")
	if secondReply != "second execution summary recorded" {
		t.Fatalf("second assistant reply = %q, want scripted response; frames=%v\n%s", secondReply, eventTypes(secondEvents), h.proc.LogTail(60))
	}
	if reqs := fake.requests(); len(reqs) != 2 {
		t.Fatalf("fake received %d model requests, want exactly two text-only requests", len(reqs))
	} else {
		for i, req := range reqs {
			joined := strings.Join(req.Messages, "\n")
			if strings.Contains(joined, `"execution"`) || strings.Contains(joined, skillMarker) || strings.Contains(joined, packageV1) || strings.Contains(joined, pluginID) {
				t.Errorf("provider request %d contains execution metadata or package payload: %q", i, joined)
			}
		}
	}

	// Wait for the asynchronous canonical flush, then inspect both the raw row
	// and the public transcript. The raw assertion catches a serializer that
	// happens to produce the right view while dropping the durable metadata.
	before := h.waitExecutionSummaryMessages(t, ctx, agentID, sessionID, pluginID, packageV1, skillName)
	var beforeExecutionJSON []byte
	var anchorMessageID string
	for _, message := range before.Messages {
		if message.Role == apitypes.SessionMessageRoleUser && message.Execution != nil {
			anchorMessageID = message.Id
			beforeExecutionJSON, _ = json.Marshal(message.Execution)
			break
		}
	}
	if anchorMessageID == "" || len(beforeExecutionJSON) == 0 {
		t.Fatal("transcript had no user execution summary to freeze")
	}
	var userMetadata []byte
	if err := h.db.QueryRow(ctx, `
		SELECT execution_metadata
		  FROM ctx_message m
		  JOIN ctx_conversation c ON c.id = m.conversation_id
		 WHERE c.session_id = $1 AND m.id = $2 AND m.role = 'user'
		 LIMIT 1`, sessionID, anchorMessageID).Scan(&userMetadata); err != nil {
		t.Fatalf("query user execution_metadata: %v", err)
	}
	if len(userMetadata) == 0 || !bytes.Contains(userMetadata, []byte(pluginID)) || !bytes.Contains(userMetadata, []byte(packageV1)) {
		t.Fatalf("user execution_metadata = %s, want immutable package identity", userMetadata)
	}
	if bytes.Contains(userMetadata, []byte(skillMarker)) {
		t.Fatal("user execution metadata persisted private Skill body")
	}

	// Replace the package definition through its preview/CAS API, then kill and
	// restart the real server. The old transcript must retain packageV1 even
	// though all future admissions would see packageV2.
	secondSource := executionSummaryPackage(t, pluginID, packageV2, skillName, "replacement package body")
	previewResponse := h.postJSON(t, ctx, "/api/plugins/"+pluginID+"/update/preview", map[string]any{
		"source_path": secondSource, "expected_revision": *created.Plugin.Revision,
	})
	defer func() { _ = previewResponse.Body.Close() }()
	if previewResponse.StatusCode != http.StatusOK {
		t.Fatalf("preview package update = %d, want %d\n%s", previewResponse.StatusCode, http.StatusOK, h.proc.LogTail(60))
	}
	var preview apitypes.PreviewPluginPackageResponse
	if err := json.NewDecoder(previewResponse.Body).Decode(&preview); err != nil {
		t.Fatalf("decode package preview: %v", err)
	}
	if preview.CandidateVersion != packageV2 || preview.CandidateDigest == "" {
		t.Fatalf("package preview = %#v, want version %q and digest", preview, packageV2)
	}
	updateResponse := h.postJSON(t, ctx, "/api/plugins/"+pluginID+"/update", map[string]any{
		"source_path": secondSource, "expected_revision": *created.Plugin.Revision,
		"expected_package_digest": preview.CandidateDigest,
	})
	defer func() { _ = updateResponse.Body.Close() }()
	if updateResponse.StatusCode != http.StatusOK {
		t.Fatalf("package update = %d, want %d\n%s", updateResponse.StatusCode, http.StatusOK, h.proc.LogTail(60))
	}

	h.restartAfterForcedCrash(t)
	ctxAfterRestart, cancelAfterRestart := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelAfterRestart()
	afterRestart := h.getSessionMessages(t, ctxAfterRestart, agentID, sessionID)
	for _, message := range afterRestart.Messages {
		if message.Id != anchorMessageID || message.Execution == nil {
			continue
		}
		assertExecutionSummary(t, message.Execution, pluginID, packageV1, skillName)
		afterExecutionJSON, _ := json.Marshal(message.Execution)
		if !bytes.Equal(afterExecutionJSON, beforeExecutionJSON) {
			t.Fatalf("execution summary changed across package update/restart: before=%s after=%s", beforeExecutionJSON, afterExecutionJSON)
		}
		return
	}
	t.Fatalf("transcript after package update/restart contains no execution summary: %#v", afterRestart.Messages)
}

func executionSummaryPackage(t *testing.T, pluginID, version, skillName, body string) string {
	t.Helper()
	root := t.TempDir()
	manifest := fmt.Sprintf(`{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":%q,"version":%q,"description":"execution summary system package","extensions":{"com.cherryhq.stella":{"version":"1"}}}`, pluginID, version)
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(root, "skills", skillName)
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	skill := fmt.Sprintf("---\nname: %s\ndescription: Records execution summary identity.\n---\n\n# Execution canary\n\n%s\n", skillName, body)
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skill), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func (h *harness) waitExecutionSummaryMessages(t *testing.T, ctx context.Context, agentID, sessionID, pluginID, packageVersion, skillName string) apitypes.SessionMessageList {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var last apitypes.SessionMessageList
	for {
		last = h.getSessionMessages(t, ctx, agentID, sessionID)
		for _, message := range last.Messages {
			if message.Execution != nil {
				assertExecutionSummary(t, message.Execution, pluginID, packageVersion, skillName)
				return last
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("execution summary not visible in transcript: %#v", last.Messages)
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func (h *harness) getSessionMessages(t *testing.T, ctx context.Context, agentID, sessionID string) apitypes.SessionMessageList {
	t.Helper()
	path := fmt.Sprintf("/api/agents/%s/sessions/%s/messages?limit=50", agentID, sessionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.baseURL+path, nil)
	if err != nil {
		t.Fatalf("build transcript request: %v", err)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("GET transcript: %v\n%s", err, h.proc.LogTail(60))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET transcript = %d, want %d\n%s", resp.StatusCode, http.StatusOK, h.proc.LogTail(60))
	}
	var result apitypes.SessionMessageList
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode transcript: %v", err)
	}
	return result
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func assertExecutionSummary(t *testing.T, summary *apitypes.SessionExecutionSummary, pluginID, packageVersion, skillName string) {
	t.Helper()
	if summary == nil {
		t.Fatal("execution summary is nil")
	}
	var plugin *apitypes.SessionExecutionPlugin
	for i := range summary.Plugins {
		if summary.Plugins[i].PluginId == pluginID {
			plugin = &summary.Plugins[i]
			break
		}
	}
	if plugin == nil || plugin.PackageVersion == nil || *plugin.PackageVersion != packageVersion {
		t.Fatalf("execution plugins = %#v, want package %s@%s", summary.Plugins, pluginID, packageVersion)
	}
	if summary.Skills == nil {
		t.Fatalf("execution skills missing, want %s/%s@%s", pluginID, skillName, packageVersion)
	}
	for _, skill := range *summary.Skills {
		if valueOrEmpty(skill.PluginId) == pluginID && skill.Name == skillName && valueOrEmpty(skill.Version) == packageVersion {
			return
		}
	}
	t.Fatalf("execution skills = %#v, want %s/%s@%s", *summary.Skills, pluginID, skillName, packageVersion)
}
