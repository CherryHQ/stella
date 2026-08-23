package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/agent"
	coreagent "github.com/CherryHQ/stella/pkg/agent"
)

func TestStatusReportsActiveSandboxBackendToUnauthenticatedCallers(t *testing.T) {
	t.Setenv("STELLA_SANDBOX_BACKEND", "bridge")
	rr := httptest.NewRecorder()
	(&Server{}).GetStatus(rr, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var got types.StatusResponse
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.SandboxBackend == nil || *got.SandboxBackend != "bridge" {
		t.Fatalf("sandbox_backend = %v, want bridge", got.SandboxBackend)
	}
}

func TestStatusReportsActiveAgentToolMode(t *testing.T) {
	// Server construction owns the immutable mode; the endpoint must not read
	// mutable process environment while serving an eval's evidence request.
	pool := agent.NewPoolManager(nil, nil, agent.WithToolMode(coreagent.ToolModeCode))
	rr := httptest.NewRecorder()
	(&Server{poolManager: pool}).GetStatus(rr, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	var got types.StatusResponse
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.AgentToolMode == nil || *got.AgentToolMode != "code" {
		t.Fatalf("agent_tool_mode = %v, want code", got.AgentToolMode)
	}
}
