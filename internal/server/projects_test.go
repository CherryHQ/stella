package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/skills"
)

func TestProjectResponseReportsUnavailableCoordinate(t *testing.T) {
	encoded, err := json.Marshal(toProjectResponse(agent.Project{ID: "legacy", IsUnavailable: true}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"base_dir":""`) || !strings.Contains(string(encoded), `"is_unavailable":true`) {
		t.Fatalf("unavailable Project response = %s", encoded)
	}
}

func TestManagedSkillUnavailableMapsToServiceUnavailable(t *testing.T) {
	recorder := httptest.NewRecorder()
	new(Server).writeManagedSkillError(recorder, errors.Join(skills.ErrManagedSkillsUnavailable, skills.ErrManagedSkillsPending))
	if recorder.Code != http.StatusServiceUnavailable || recorder.Header().Get("Retry-After") != "5" || !strings.Contains(recorder.Body.String(), "retry shortly") {
		t.Fatalf("managed Skill unavailable response = %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	new(Server).writeManagedSkillError(recorder, errors.Join(skills.ErrManagedSkillsUnavailable, skills.ErrSkillMigrationData))
	if recorder.Code != http.StatusServiceUnavailable || recorder.Header().Get("Retry-After") != "" || !strings.Contains(recorder.Body.String(), "restart Stella") {
		t.Fatalf("managed Skill degraded response = %d %s", recorder.Code, recorder.Body.String())
	}
}
