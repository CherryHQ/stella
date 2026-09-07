package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
)

func TestResolveExhaustive256WinnerFirst(t *testing.T) {
	def := Definition{
		ID: "email", DisplayName: "Email",
		Source: SourceBuiltin, Revision: 1,
		DefaultEnabled: true, Spec: publishedSpec(t, `{"schema":1}`),
	}
	states := []struct {
		name    string
		enabled *bool
		payload json.RawMessage
	}{
		{name: "absent"},
		{name: "inherit", payload: json.RawMessage(`{}`)},
		{name: "false", enabled: boolPtr(false)},
		{name: "true", enabled: boolPtr(true), payload: json.RawMessage(`{}`)},
	}
	scopes := []Scope{ScopeSystem, ScopeSystemAgent, ScopeUser, ScopeUserAgent}
	for s := range 4 {
		for sa := range 4 {
			for u := range 4 {
				for ua := range 4 {
					configs := make([]Config, 0, 4)
					for i, state := range []int{s, sa, u, ua} {
						if state == 0 {
							continue
						}
						config := Config{
							ID: string(scopes[i]) + "-id", PluginID: def.ID,
							Scope: scopes[i], Payload: states[state].payload,
							Enabled: states[state].enabled, Revision: 1,
						}
						if scopes[i] == ScopeUser || scopes[i] == ScopeUserAgent {
							config.UserID = "user"
						}
						if scopes[i] == ScopeSystemAgent || scopes[i] == ScopeUserAgent {
							config.AgentID = "agent"
						}
						configs = append(configs, config)
					}
					got, err := Resolve(def, configs, "user", "agent")
					if err != nil {
						t.Fatalf("states %d/%d/%d/%d: %v", s, sa, u, ua, err)
					}
					want := states[0]
					wantScope := Scope("")
					for i, state := range []int{ua, u, sa, s} {
						if state != 0 {
							want = states[state]
							wantScope = []Scope{ScopeUserAgent, ScopeUser, ScopeSystemAgent, ScopeSystem}[i]
							break
						}
					}
					wantEnabled := def.DefaultEnabled
					if want.enabled != nil {
						wantEnabled = *want.enabled
					}
					if s == 2 || sa == 2 {
						wantEnabled = false
						if s == 2 {
							wantScope = ScopeSystem
						} else {
							wantScope = ScopeSystemAgent
						}
					}
					if got.IsEffectivelyEnabled != wantEnabled || got.SourceScope != wantScope {
						t.Fatalf("states %d/%d/%d/%d: got enabled=%v scope=%q, want enabled=%v scope=%q", s, sa, u, ua, got.IsEffectivelyEnabled, got.SourceScope, wantEnabled, wantScope)
					}
				}
			}
		}
	}
}

func TestResolveCapsCannotBeBypassed(t *testing.T) {
	def := testDefinition()
	falseValue := false
	trueValue := true
	configs := []Config{
		{ID: "system", PluginID: def.ID, Scope: ScopeSystem, Enabled: &falseValue, Revision: 1},
		{ID: "agent", PluginID: def.ID, Scope: ScopeSystemAgent, AgentID: "agent", Enabled: &trueValue, Payload: json.RawMessage(`{}`), Revision: 1},
		{ID: "user-agent", PluginID: def.ID, Scope: ScopeUserAgent, UserID: "user", AgentID: "agent", Enabled: &trueValue, Payload: json.RawMessage(`{}`), Revision: 1},
	}
	got, err := Resolve(def, configs, "user", "agent")
	if err != nil {
		t.Fatal(err)
	}
	if got.IsEffectivelyEnabled || got.SourceScope != ScopeSystem {
		t.Fatalf("system cap = %#v, want disabled system winner", got)
	}
}

func TestResolveRejectsMismatchedOwner(t *testing.T) {
	def := testDefinition()
	value := true
	got, err := Resolve(def, []Config{{ID: "u", PluginID: def.ID, Scope: ScopeUser, UserID: "other", Enabled: &value, Payload: json.RawMessage(`{}`), Revision: 1}}, "user", "agent")
	if err != nil {
		t.Fatal(err)
	}
	if got.IsEffectivelyEnabled || got.SourceScope != "" {
		t.Fatalf("mismatched owner was selected: %#v", got)
	}
}

func TestResolveAbsentUsesShippedPayload(t *testing.T) {
	def := testDefinition()
	def.DefaultEnabled = true
	def.Spec = publishedSpec(t, `{"description":"shipped"}`)
	got, err := Resolve(def, nil, "u", "")
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Payload) != string(def.Spec) {
		t.Fatalf("shipped payload = %s, want %s", got.Payload, def.Spec)
	}
	def.Spec[16] = 'X'
	if string(got.Payload) != string(publishedSpec(t, `{"description":"shipped"}`)) {
		t.Fatalf("effective payload retained definition alias: %s", got.Payload)
	}
}

func TestCustomDefinitionCannotDefaultEnabled(t *testing.T) {
	def := testDefinition()
	def.Source, def.DefaultEnabled = SourceCustom, true
	if err := def.Validate(); err == nil {
		t.Fatal("custom definition defaulted enabled")
	}
}

func TestResolveRejectsRetiredDefinition(t *testing.T) {
	def := testDefinition()
	def.Source, def.DefaultEnabled = SourceCustom, false
	def.RetiredAt = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	effective, err := Resolve(def, nil, "user", "agent")
	if err != nil || effective.IsEffectivelyEnabled || effective.AvailabilityReason != "retired" {
		t.Fatalf("retired definition = %#v, %v; want disabled retired", effective, err)
	}
}

func TestResolveResourceSourceMatrix(t *testing.T) {
	resources := []struct {
		name string
		spec json.RawMessage
	}{
		{name: "prompt", spec: json.RawMessage(`{"prompt":"guide"}`)},
		{name: "cli", spec: json.RawMessage(`{"binaries":[{"name":"tool","tool":"uv","version":"1"}],"session_env":[{"env_var":"TOKEN","source":"oauth.access_token"}]}`)},
		{name: "mcp", spec: json.RawMessage(`{"mcp_servers":{"remote":{"url":"https://example.test","transport":"sse","auth_type":"none"}}}`)},
	}
	sources := []Source{SourceBuiltin, SourceCustom}
	for _, resource := range resources {
		for _, source := range sources {
			for _, defaultEnabled := range []bool{false, true} {
				name := resource.name + "/" + string(source) + "/" + fmt.Sprint(defaultEnabled)
				t.Run(name, func(t *testing.T) {
					def := Definition{
						ID: "matrix-" + resource.name + "-" + string(source) + "-" + fmt.Sprint(defaultEnabled), DisplayName: name,
						Source: source, Spec: publishedSpec(t, string(resource.spec)),
						DefaultEnabled: defaultEnabled, Revision: 1,
					}
					if source == SourceCustom {
						def.DefaultEnabled = false
					}
					if err := def.Validate(); err != nil {
						t.Fatal(err)
					}
					if string(def.Spec) != string(publishedSpec(t, string(resource.spec))) {
						t.Fatalf("resource spec changed for %s: %s", resource.name, def.Spec)
					}
					got, err := Resolve(def, nil, "user", "agent")
					if err != nil {
						t.Fatal(err)
					}
					if got.IsEffectivelyEnabled != def.DefaultEnabled {
						t.Fatalf("default enabled = %v, want %v", got.IsEffectivelyEnabled, def.DefaultEnabled)
					}
					trueValue, falseValue := true, false
					got, err = Resolve(def, []Config{
						{ID: "system", PluginID: def.ID, Scope: ScopeSystem, Enabled: &falseValue, Revision: 1},
						{ID: "user-agent", PluginID: def.ID, Scope: ScopeUserAgent, UserID: "user", AgentID: "agent", Enabled: &trueValue, Payload: json.RawMessage(`{}`), Revision: 1},
					}, "user", "agent")
					if err != nil {
						t.Fatal(err)
					}
					if got.IsEffectivelyEnabled || got.SourceScope != ScopeSystem {
						t.Fatalf("system deny bypassed for %s: %#v", name, got)
					}
				})
			}
		}
	}
}

func TestResolveByPluginIDChoosesPayloadOwnerByScope(t *testing.T) {
	systemDef := testDefinition()
	userDef := systemDef
	userDef.ID = "user"
	userEnabled := true
	got, err := Resolve(userDef, []Config{{ID: "user", PluginID: userDef.ID, Scope: ScopeUser, UserID: "u", Enabled: &userEnabled, Payload: json.RawMessage(`{"private":true}`), Revision: 1}}, "u", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.PluginID != userDef.ID || got.SourceScope != ScopeUser {
		t.Fatalf("namespace winner = %#v", got)
	}
	if string(got.Payload) != `{"private":true}` {
		t.Fatalf("plugin payload = %s", got.Payload)
	}
}

func TestResolvePreservesWinningDefinitionCaps(t *testing.T) {
	first := testDefinition()
	second := first
	second.ID = "lower"
	falseValue, trueValue := false, true
	configs := []Config{
		{ID: "first-system", PluginID: first.ID, Scope: ScopeSystem, Enabled: &falseValue, Revision: 1},
		{ID: "first-ua", PluginID: first.ID, Scope: ScopeUserAgent, UserID: "u", AgentID: "a", Enabled: &trueValue, Payload: json.RawMessage(`{"ua":true}`), Revision: 1},
	}
	got, err := Resolve(first, configs, "u", "a")
	if err != nil {
		t.Fatal(err)
	}
	if got.PluginID != first.ID || got.IsEffectivelyEnabled || got.SourceScope != ScopeSystem {
		t.Fatalf("winning definition caps were bypassed: %#v", got)
	}
}

func TestCatalogAllowsDistinctDefinitions(t *testing.T) {
	catalog := NewCatalog()
	first := testDefinition()
	second := first
	second.ID = "two"
	if err := catalog.Register(first); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Register(second); err != nil {
		t.Fatal(err)
	}
	if got, ok := catalog.Get(first.ID); !ok || got.ID != first.ID {
		t.Fatal("definition ID lookup failed")
	}
}

func TestNameContract(t *testing.T) {
	for _, name := range []string{"email-2", "mcp-remote"} {
		if err := ValidateName(name); err != nil {
			t.Fatalf("name %q rejected: %v", name, err)
		}
	}
	for _, name := range []string{"has/slash", "bad__separator", ""} {
		if err := ValidateName(name); err == nil {
			t.Fatalf("name %q accepted", name)
		}
	}
}

func TestDefinitionValidationDoesNotNormalizeEmptySpec(t *testing.T) {
	def := testDefinition()
	def.Spec = nil
	if err := def.Validate(); err == nil {
		t.Fatal("empty spec accepted")
	}
	if def.Spec != nil {
		t.Fatalf("Validate mutated empty spec to %s", def.Spec)
	}
}

func TestCatalogAndResolverDefensivelyCopyMutableFields(t *testing.T) {
	spec := publishedSpec(t, `{"binaries":[{"name":"tool","tool":"uv","options":{"channel":"stable"}}]}`)
	original := append(json.RawMessage(nil), spec...)
	def := testDefinition()
	def.Spec = spec
	catalog := NewCatalog()
	if err := catalog.Register(def); err != nil {
		t.Fatal(err)
	}
	spec[8] = 'X'
	got, ok := catalog.Get(def.ID)
	if !ok || string(got.Spec) != string(original) {
		t.Fatalf("catalog retained caller alias: %s", got.Spec)
	}
	got.Spec[8] = 'Y'
	again, _ := catalog.Get(def.ID)
	if string(again.Spec) != string(original) {
		t.Fatalf("catalog returned internal alias: %s", again.Spec)
	}
	def.Spec = append(json.RawMessage(nil), original...)
	value := true
	payload := json.RawMessage(`{"binaries":{"tool":{"options":{"channel":"custom"}}}}`)
	config := Config{ID: "c", PluginID: def.ID, Scope: ScopeUser, UserID: "u", Enabled: &value, Payload: payload, Revision: 1, CreatedAt: time.Now().UTC()}
	effective, err := Resolve(def, []Config{config}, "u", "")
	if err != nil {
		t.Fatal(err)
	}
	channelOffset := bytes.Index(payload, []byte("custom"))
	if channelOffset < 0 {
		t.Fatal("fixture has no custom channel")
	}
	copy(payload[channelOffset:], "mutant")
	if string(effective.Payload) == "" {
		t.Fatal("resolver returned empty payload")
	}
	var effectivePayload ResourcePayload
	if err := json.Unmarshal(effective.Payload, &effectivePayload); err != nil || len(effectivePayload.Binaries) != 1 || effectivePayload.Binaries[0].Options["channel"] != "custom" {
		t.Fatalf("resolver retained caller alias: %s", effective.Payload)
	}
}

func TestAccessDerivesOnlyTrustedUserScope(t *testing.T) {
	authority, err := authz.NewUserAuthority("user", false)
	if err != nil {
		t.Fatal(err)
	}
	access := &Access{service: &Service{}, authority: authority}
	userID, agentID, err := access.owner(t.Context(), ScopeUser, "")
	if err != nil || userID != "user" || agentID != "" {
		t.Fatalf("owner = %q/%q, err=%v", userID, agentID, err)
	}
	if _, _, err := access.owner(t.Context(), ScopeUser, "attacker"); err == nil {
		t.Fatal("user scope accepted an agent owner")
	}
	if _, _, err := access.owner(t.Context(), ScopeUserAgent, "agent"); err == nil {
		t.Fatal("agent scope bypassed the central Agent PEP")
	}
}

func testDefinition() Definition {
	return Definition{ID: "test", DisplayName: "Test", Source: SourceBuiltin, Spec: publishedSpecOrPanic(`{}`), Revision: 1}
}

func boolPtr(value bool) *bool { return &value }
