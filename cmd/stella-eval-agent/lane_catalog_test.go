package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestSpecializedTasksShareTheFrozenLaneCatalog(t *testing.T) {
	plan := fixtureConfig{CatalogDigest: "sha256:catalog"}
	inspection := fixtureInspection{
		Version: 1, Complete: true, CatalogCount: specializedCatalogCount,
		InitializeCount: 1, InitializedNotificationCount: 1, ToolsListCount: 1,
	}
	providerTools := []string{"bash", "skills", "memory", "library_search", "recally"}
	for i := range specializedCatalogCount {
		providerTools = append(providerTools, fmt.Sprintf("mcp__harbor-specialized-fixture__tool_%02d", i))
	}
	wantCapability := digestProfile([]string{"share"}, "bundle")
	var first result
	for _, task := range []specializedTask{taskSkillBashGuard, taskMemoryLibraryEvidence, taskMCPRecally} {
		got := result{
			ToolStrategy:                    "native",
			ProviderSurfaceCount:            len(providerTools),
			ProviderSurfaceTools:            append([]string(nil), providerTools...),
			ProviderSurfaceDigest:           "sha256:provider-surface",
			CapabilityProfileDigest:         wantCapability,
			MCPRegistrationID:               "registration-" + string(task),
			MCPTools:                        []string{specializedFixtureRegistrationName},
			SpecializedCatalogCount:         specializedCatalogCount,
			SpecializedCatalogDigest:        plan.CatalogDigest,
			RuntimeSpecializedCatalogDigest: "sha256:runtime-specialized-catalog",
		}
		if err := assertRuntimeLaneCatalog(got); err != nil {
			t.Fatalf("%s runtime lane catalog: %v", task, err)
		}
		if err := assertMCPFixtureAdmission(got, plan, inspection); err != nil {
			t.Fatalf("%s MCP admission: %v", task, err)
		}
		if first.ToolStrategy == "" {
			first = got
			continue
		}
		if got.ProviderSurfaceDigest != first.ProviderSurfaceDigest ||
			got.RuntimeSpecializedCatalogDigest != first.RuntimeSpecializedCatalogDigest ||
			got.CapabilityProfileDigest != first.CapabilityProfileDigest {
			t.Fatalf("%s changed a lane-wide catalog digest: %+v", task, got)
		}
	}
}

func TestSpecializedLanePolicyKeepsOnlyTheFrozenBuiltinUnion(t *testing.T) {
	tools := []agentTool{
		{Name: "bash", Source: "core", Enabled: true},
		{Name: "skills", Source: "builtin", Enabled: true},
		{Name: "memory", Source: "builtin", Enabled: true},
		{Name: "library_search", Source: "builtin", Enabled: true},
		{Name: "recally", Source: "builtin", Enabled: true},
		{Name: "share", Source: "builtin", Enabled: false},
		{Name: specializedFixtureRegistrationName, Source: "mcp", Enabled: true},
	}
	if err := assertLaneToolPolicy(tools); err != nil {
		t.Fatalf("frozen lane policy rejected: %v", err)
	}
	tools[5].Enabled = true
	if err := assertLaneToolPolicy(tools); err == nil {
		t.Fatal("enabled Share was admitted to the frozen lane")
	}
	tools[5].Enabled = false
	tools = append(tools, agentTool{Name: "other-mcp", Source: "mcp", Enabled: true})
	if err := assertLaneToolPolicy(tools); err == nil {
		t.Fatal("multiple MCP registrations were admitted")
	}
}

func TestSpecializedLaneEnablesFreshDefaultDisabledBuiltins(t *testing.T) {
	initial := []agentTool{
		{Name: "bash", Source: "core", Enabled: true},
		{Name: "skills", Source: "builtin", Enabled: false},
		{Name: "memory", Source: "builtin", Enabled: false},
		{Name: "library_search", Source: "builtin", Enabled: false},
		{Name: "recally", Source: "builtin", Enabled: false},
		{Name: "share", Source: "builtin", Enabled: false},
		{Name: specializedFixtureRegistrationName, Source: "mcp", Enabled: true},
	}
	var wantSurface []agentTool
	var wantDisabled []string
	var wantDigest string
	for _, task := range []specializedTask{taskSkillBashGuard, taskMemoryLibraryEvidence, taskMCPRecally} {
		tools := append([]agentTool(nil), initial...)
		patches := map[string]bool{}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				_ = json.NewEncoder(w).Encode(map[string]any{"tools": tools})
			case http.MethodPatch:
				name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
				var request struct {
					Enabled bool   `json:"enabled"`
					Scope   string `json:"scope"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				if request.Scope != "user_agent" {
					t.Fatalf("%s PATCH scope = %q, want user_agent", task, request.Scope)
				}
				patches[name] = request.Enabled
				for i := range tools {
					if tools[i].Name == name {
						tools[i].Enabled = request.Enabled
					}
				}
				w.WriteHeader(http.StatusOK)
			default:
				t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
			}
		}))
		client := apiClient{baseURL: server.URL, http: server.Client()}
		final, disabled, err := configureSpecializedToolPolicy(context.Background(), client, "agent", tools)
		server.Close()
		if err != nil {
			t.Fatalf("%s policy: %v", task, err)
		}
		wantPatches := map[string]bool{"skills": true, "memory": true, "library_search": true, "recally": true, "share": false}
		if !reflect.DeepEqual(patches, wantPatches) {
			t.Fatalf("%s PATCHes = %#v, want %#v", task, patches, wantPatches)
		}
		if err := assertLaneToolPolicy(final); err != nil {
			t.Fatalf("%s final policy: %v", task, err)
		}
		if wantSurface == nil {
			wantSurface, wantDisabled, wantDigest = final, disabled, digestProfile(disabled, "bundle")
			continue
		}
		if !reflect.DeepEqual(final, wantSurface) || !reflect.DeepEqual(disabled, wantDisabled) || digestProfile(disabled, "bundle") != wantDigest {
			t.Fatalf("%s changed the frozen tool surface or capability digest", task)
		}
	}
}

func TestSpecializedLaneFailsWhenPolicyPatchFails(t *testing.T) {
	tools := []agentTool{
		{Name: "bash", Source: "core", Enabled: true},
		{Name: "skills", Source: "builtin", Enabled: false},
		{Name: "memory", Source: "builtin", Enabled: true},
		{Name: "library_search", Source: "builtin", Enabled: true},
		{Name: "recally", Source: "builtin", Enabled: true},
		{Name: specializedFixtureRegistrationName, Source: "mcp", Enabled: true},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || !strings.HasSuffix(r.URL.Path, "/tools/skills") {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		http.Error(w, "unavailable", http.StatusInternalServerError)
	}))
	defer server.Close()
	client := apiClient{baseURL: server.URL, http: server.Client()}
	if _, _, err := configureSpecializedToolPolicy(context.Background(), client, "agent", tools); err == nil || !strings.Contains(err.Error(), `set tool "skills" enabled=true: HTTP 500`) {
		t.Fatalf("PATCH failure = %v", err)
	}
}

func TestSpecializedLaneFailsWhenCatalogToolIsGloballyAbsent(t *testing.T) {
	tools := []agentTool{
		{Name: "bash", Source: "core", Enabled: true},
		{Name: "memory", Source: "builtin", Enabled: false},
		{Name: "library_search", Source: "builtin", Enabled: false},
		{Name: "recally", Source: "builtin", Enabled: false},
		{Name: specializedFixtureRegistrationName, Source: "mcp", Enabled: true},
	}
	if _, _, err := configureSpecializedToolPolicy(context.Background(), apiClient{}, "agent", tools); err == nil || !strings.Contains(err.Error(), `lane catalog tool "skills" is absent`) {
		t.Fatalf("missing catalog tool error = %v", err)
	}
}

func TestNonMCPVerifiersNeedCausalEvidenceNotMCPCalls(t *testing.T) {
	plan := fixtureConfig{CatalogDigest: "sha256:catalog"}
	admission := result{
		MCPRegistrationID:               "registration",
		MCPTools:                        []string{specializedFixtureRegistrationName},
		SpecializedCatalogCount:         specializedCatalogCount,
		SpecializedCatalogDigest:        plan.CatalogDigest,
		RuntimeSpecializedCatalogDigest: "sha256:runtime-specialized-catalog",
	}
	complete := fixtureInspection{Version: 1, Complete: true, CatalogCount: specializedCatalogCount, InitializeCount: 1, InitializedNotificationCount: 1, ToolsListCount: 1}
	if err := assertMCPFixtureAdmission(admission, plan, complete); err != nil {
		t.Fatalf("complete catalog admission: %v", err)
	}
	incomplete := complete
	incomplete.InitializedNotificationCount = 0
	if err := assertMCPFixtureAdmission(admission, plan, incomplete); err == nil {
		t.Fatal("missing notifications/initialized was admitted for a non-MCP task")
	}
	incomplete = complete
	incomplete.ToolsListCount = 0
	if err := assertMCPFixtureAdmission(admission, plan, incomplete); err == nil {
		t.Fatal("missing tools/list was admitted for a non-MCP task")
	}

	skill, _, err := newSpecializedFixture(taskSkillBashGuard, "fixture-plan-seed")
	if err != nil {
		t.Fatal(err)
	}
	if verdict, err := verifySkillBashGuard(t.Context(), bridgeArtifactBinding(t, skill.artifact), skill); err != nil || verdict.Reward != 1 {
		t.Fatalf("skill verifier = %+v, %v", verdict, err)
	}
	memory, _, err := newSpecializedFixture(taskMemoryLibraryEvidence, "fixture-plan-seed")
	if err != nil {
		t.Fatal(err)
	}
	if verdict, err := verifyMemoryLibraryEvidence(t.Context(), bridgeArtifactBinding(t, memory.artifact), memory); err != nil || verdict.Reward != 1 {
		t.Fatalf("memory verifier = %+v, %v", verdict, err)
	}
}
