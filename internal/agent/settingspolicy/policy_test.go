package settingspolicy

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/store"
)

type adminLookup struct {
	admin bool
	err   error
}

func (l adminLookup) IsAdmin(context.Context, string) (bool, error) { return l.admin, l.err }

func TestAvailableRequiresForegroundStellaDirectSession(t *testing.T) {
	available := Available(false, nil)
	base := runtime.RunnerParams{UserID: "u", AgentID: store.DefaultStellaAgentID, ForegroundHuman: true}
	for name, params := range map[string]runtime.RunnerParams{
		"direct Stella":  base,
		"ordinary agent": {UserID: "u", AgentID: "other", ForegroundHuman: true},
		"group":          {UserID: "u", AgentID: store.DefaultStellaAgentID, GroupID: "g", ForegroundHuman: true},
		"guest":          {UserID: "u", AgentID: store.DefaultStellaAgentID, GuestID: "g", ForegroundHuman: true},
		"worker":         {UserID: "u", AgentID: store.DefaultStellaAgentID},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := available(t.Context(), params)
			if err != nil || got != (name == "direct Stella") {
				t.Fatalf("Available = (%t, %v)", got, err)
			}
		})
	}
}

func TestAdminAvailabilityFailsClosed(t *testing.T) {
	params := runtime.RunnerParams{UserID: "u", AgentID: store.DefaultStellaAgentID, ForegroundHuman: true}
	if got, err := Available(true, adminLookup{err: errors.New("down")})(t.Context(), params); err == nil || got {
		t.Fatalf("failed lookup = (%t, %v), want false and error", got, err)
	}
	if got, err := Available(true, adminLookup{admin: true})(t.Context(), params); err != nil || !got {
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
