package settingspolicy

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

type adminLookup struct {
	admin bool
	err   error
}

func (l adminLookup) IsAdmin(context.Context, string) (bool, error) { return l.admin, l.err }

type agentLookup struct {
	agents map[string]config.Agent
	err    error
}

func (l *agentLookup) GetAgent(_ context.Context, id string) (config.Agent, error) {
	if l.err != nil {
		return config.Agent{}, l.err
	}
	agent, ok := l.agents[id]
	if !ok {
		return config.Agent{}, errors.New("not found")
	}
	return agent, nil
}

type countingTool struct{ calls int }

func (t *countingTool) Definition() pkgtools.Definition {
	return pkgtools.Definition{Name: "agent_list"}
}

func (t *countingTool) Execute(context.Context, map[string]any) (string, error) {
	t.calls++
	return "ok", nil
}

func TestCatalogOwnsEverySettingsActionAndFamily(t *testing.T) {
	entries := Catalog()
	if len(entries) != 34 {
		t.Fatalf("Settings catalog length = %d, want 34", len(entries))
	}
	counts := map[string]int{}
	seen := map[string]bool{}
	adminCount := 0
	for _, entry := range entries {
		if entry.Name == "" || seen[entry.Name] {
			t.Fatalf("invalid or duplicate Settings action %#v", entry)
		}
		seen[entry.Name] = true
		counts[entry.Family]++
		if entry.AdminRequired {
			adminCount++
		}
		if got, ok := Lookup(entry.Name); !ok || got != entry {
			t.Fatalf("Lookup(%q) = %#v, %t; want %#v, true", entry.Name, got, ok, entry)
		}
	}
	wantCounts := map[string]int{
		FamilyAgentManagement:       8,
		FamilyKnowledgeAndSkills:    9,
		FamilyModelsAndDeployment:   9,
		FamilyExtensionsAndConnects: 8,
	}
	for family, want := range wantCounts {
		if got := counts[family]; got != want {
			t.Errorf("%s actions = %d, want %d", family, got, want)
		}
	}
	if adminCount != 12 {
		t.Fatalf("admin Settings actions = %d, want 12", adminCount)
	}
}

func TestAvailableRequiresEnabledForegroundDirectSession(t *testing.T) {
	lookup := &agentLookup{agents: map[string]config.Agent{
		"enabled":  {ID: "enabled", SystemSettingsToolsEnabled: true},
		"disabled": {ID: "disabled", SystemSettingsToolsEnabled: false},
	}}
	available := Available(false, nil, lookup)
	for name, params := range map[string]runtime.RunnerParams{
		"direct enabled agent":   {UserID: "u", AgentID: "enabled", ForegroundHuman: true},
		"disabled agent":         {UserID: "u", AgentID: "disabled", ForegroundHuman: true},
		"group":                  {UserID: "u", AgentID: "enabled", GroupID: "g", ForegroundHuman: true},
		"guest":                  {UserID: "u", AgentID: "enabled", GuestID: "g", ForegroundHuman: true},
		"worker or session send": {UserID: "u", AgentID: "enabled"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := available(t.Context(), params)
			if err != nil || got != (name == "direct enabled agent") {
				t.Fatalf("Available = (%t, %v)", got, err)
			}
		})
	}
}

func TestAvailabilityFailsClosedWhenDurablePolicyCannotBeRead(t *testing.T) {
	available := Available(false, nil, &agentLookup{err: errors.New("database unavailable")})
	got, err := available(t.Context(), runtime.RunnerParams{UserID: "u", AgentID: "a", ForegroundHuman: true})
	if err == nil || got {
		t.Fatalf("unreadable policy = (%t, %v), want false and error", got, err)
	}
}

func TestAdminAvailabilityFailsClosed(t *testing.T) {
	lookup := &agentLookup{agents: map[string]config.Agent{"a": {ID: "a", SystemSettingsToolsEnabled: true}}}
	params := runtime.RunnerParams{UserID: "u", AgentID: "a", ForegroundHuman: true}
	if got, err := Available(true, adminLookup{err: errors.New("down")}, lookup)(t.Context(), params); err == nil || got {
		t.Fatalf("failed lookup = (%t, %v), want false and error", got, err)
	}
	if got, err := Available(true, adminLookup{admin: true}, lookup)(t.Context(), params); err != nil || !got {
		t.Fatalf("admin lookup = (%t, %v), want true nil", got, err)
	}
}

func TestDirectAuthorityRequiresMatchingHuman(t *testing.T) {
	authority, err := authz.NewUserAuthority("u", false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := authz.WithAuthority(context.Background(), authority)
	if got, err := DirectAuthority(ctx, "u"); err != nil || got != authority {
		t.Fatalf("DirectAuthority = (%v, %v)", got, err)
	}
	if _, err := DirectAuthority(ctx, "other"); !errors.Is(err, authz.ErrUnauthenticated) {
		t.Fatalf("mismatch error = %v", err)
	}
}

func TestWrappedToolRejectsRevokedCachedRunner(t *testing.T) {
	lookup := &agentLookup{agents: map[string]config.Agent{"a": {ID: "a", SystemSettingsToolsEnabled: true}}}
	inner := &countingTool{}
	tool := Wrap(inner, lookup)
	authority, err := authz.NewUserAuthority("u", false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := authz.WithAuthority(authz.WithAgentID(authz.WithUserID(context.Background(), "u"), "a"), authority)
	if _, err := tool.Execute(ctx, nil); err != nil || inner.calls != 1 {
		t.Fatalf("enabled execution = (%v, calls=%d), want nil and one call", err, inner.calls)
	}
	// The wrapper survives in the old runner, but the durable revoke wins.
	lookup.agents["a"] = config.Agent{ID: "a", SystemSettingsToolsEnabled: false}
	if _, err := tool.Execute(ctx, nil); !errors.Is(err, errDisabled) || inner.calls != 1 {
		t.Fatalf("revoked cached execution = (%v, calls=%d), want disabled error and one call", err, inner.calls)
	}
}
