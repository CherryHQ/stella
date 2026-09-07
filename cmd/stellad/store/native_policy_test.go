package store_test

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/internal/plugin"
)

type nativeRegistry map[string]bool

func (r nativeRegistry) NativeDefaultEnabled(id string) (bool, bool) {
	enabled, ok := r[id]
	return enabled, ok
}

func (r nativeRegistry) NativeIDs() []string {
	ids := make([]string, 0, len(r))
	for id := range r {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

type failingNativeGlobalStore struct {
	config.Store
	err error
}

func (s failingNativeGlobalStore) GetPlugin(context.Context, string) (config.Plugin, error) {
	return config.Plugin{}, s.err
}

func (s failingNativeGlobalStore) GetNativeAdmission(context.Context, string, string) (bool, bool, bool, error) {
	return false, false, false, s.err
}

func (s failingNativeGlobalStore) SetNativePluginEnabled(context.Context, string, bool) error {
	return s.err
}

func (s failingNativeGlobalStore) IsNativeAgentDenied(context.Context, string, string) (bool, error) {
	return false, s.err
}

func (s failingNativeGlobalStore) SetNativeAgentDeny(context.Context, string, string) error {
	return s.err
}

func (s failingNativeGlobalStore) DeleteNativeAgentDeny(context.Context, string, string) error {
	return s.err
}

func (s failingNativeGlobalStore) ListNativeAgentDenials(context.Context, string) ([]plugin.NativeAgentDeny, error) {
	return nil, s.err
}

type failingNativeDenyStore struct{ err error }

func (s failingNativeDenyStore) IsNativeAgentDenied(context.Context, string, string) (bool, error) {
	return false, s.err
}

func (s failingNativeDenyStore) GetNativeAdmission(context.Context, string, string) (bool, bool, bool, error) {
	return false, false, false, s.err
}

func (s failingNativeDenyStore) SetNativeAgentDeny(context.Context, string, string) error {
	return s.err
}

func (s failingNativeDenyStore) DeleteNativeAgentDeny(context.Context, string, string) error {
	return s.err
}

func (s failingNativeDenyStore) GetPlugin(context.Context, string) (config.Plugin, error) {
	return config.Plugin{}, s.err
}

func (s failingNativeDenyStore) SetNativePluginEnabled(context.Context, string, bool) error {
	return s.err
}

func (s failingNativeDenyStore) ListNativeAgentDenials(context.Context, string) ([]plugin.NativeAgentDeny, error) {
	return nil, s.err
}

type atomicAdmissionStore struct {
	config.Store
	reads int
}

func (s *atomicAdmissionStore) GetPlugin(context.Context, string) (config.Plugin, error) {
	return config.Plugin{}, nil
}

func (s *atomicAdmissionStore) SetNativePluginEnabled(context.Context, string, bool) error {
	return nil
}

func (s *atomicAdmissionStore) IsNativeAgentDenied(context.Context, string, string) (bool, error) {
	panic("non-atomic deny read")
}

func (s *atomicAdmissionStore) GetNativeAdmission(context.Context, string, string) (bool, bool, bool, error) {
	s.reads++
	return true, true, true, nil
}

func (s *atomicAdmissionStore) SetNativeAgentDeny(context.Context, string, string) error {
	return nil
}

func (s *atomicAdmissionStore) DeleteNativeAgentDeny(context.Context, string, string) error {
	return nil
}

func (s *atomicAdmissionStore) ListNativeAgentDenials(context.Context, string) ([]plugin.NativeAgentDeny, error) {
	return nil, nil
}

func TestNativeAdministrativeCapUsesAtomicAdmissionRead(t *testing.T) {
	store := &atomicAdmissionStore{}
	policy := plugin.NewNativePolicy(store, nativeRegistry{"system/email": true})
	allowed, err := policy.Allows(t.Context(), "system/email", "agent")
	if err != nil || allowed {
		t.Fatalf("atomic admission = %v, %v; want denied", allowed, err)
	}
	if store.reads != 1 {
		t.Fatalf("atomic admission reads = %d, want 1", store.reads)
	}
}

func TestNativePolicyMutationRequiresFence(t *testing.T) {
	store := &atomicAdmissionStore{}
	policy := plugin.NewNativePolicy(store, nativeRegistry{"system/email": true})
	if err := policy.SetGlobalEnabled(t.Context(), "system/email", false); !errors.Is(err, plugin.ErrNativePolicyUnavailable) {
		t.Fatalf("mutation without fence = %v, want ErrNativePolicyUnavailable", err)
	}
	policy.SetMutationFence(func(_ context.Context, mutate func() error) error { return mutate() })
	if err := policy.SetGlobalEnabled(t.Context(), "system/email", false); err != nil {
		t.Fatalf("mutation with inline fence = %v", err)
	}
}

func TestNativeAdministrativeCap(t *testing.T) {
	ctx := t.Context()
	dbStore, db := setupDBStoreWithDB(t)
	for _, agentID := range []string{"native-agent-a", "native-agent-b"} {
		if err := dbStore.CreateAgent(ctx, config.Agent{ID: agentID, Name: agentID, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	nativeID := "system/email"
	registry := nativeRegistry{nativeID: true}
	policy := plugin.NewNativePolicy(dbStore, registry)
	if err := dbStore.DeleteNativeAgentDeny(ctx, nativeID, "native-agent-missing"); !errors.Is(err, plugin.ErrNativeAgentNotFound) {
		t.Fatalf("delete missing Agent deny = %v, want ErrNativeAgentNotFound", err)
	}

	if err := dbStore.SetNativeAgentDeny(ctx, nativeID, "native-agent-a"); err != nil {
		t.Fatal(err)
	}
	if allowed, err := policy.Allows(ctx, nativeID, "native-agent-a"); err != nil || allowed {
		t.Fatalf("denied Agent cap = %v, %v; want false, nil", allowed, err)
	}
	if allowed, err := policy.Allows(ctx, nativeID, "native-agent-b"); err != nil || !allowed {
		t.Fatalf("other Agent cap = %v, %v; want true, nil", allowed, err)
	}
	if err := dbStore.DeleteNativeAgentDeny(ctx, nativeID, "native-agent-a"); err != nil {
		t.Fatal(err)
	}
	if allowed, err := policy.Allows(ctx, nativeID, "native-agent-a"); err != nil || !allowed {
		t.Fatalf("cleared Agent cap = %v, %v; want true, nil", allowed, err)
	}
	if err := dbStore.SetNativeAgentDeny(ctx, nativeID, "native-agent-a"); err != nil {
		t.Fatal(err)
	}
	if err := dbStore.DeleteAgent(ctx, "native-agent-a"); err != nil {
		t.Fatal(err)
	}
	var denies int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM native_agent_deny WHERE agent_id = $1`, "native-agent-a").Scan(&denies); err != nil {
		t.Fatal(err)
	}
	if denies != 0 {
		t.Fatalf("cascade left %d native deny rows, want 0", denies)
	}

	if err := dbStore.UpsertPlugin(ctx, config.Plugin{ID: nativeID, Kind: config.PluginKindTool, Name: "email", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if allowed, err := policy.Allows(ctx, nativeID, "native-agent-b"); err != nil || allowed {
		t.Fatalf("global deny cap = %v, %v; want false, nil", allowed, err)
	}

	storageErr := errors.New("database unavailable")
	failing := plugin.NewNativePolicy(failingNativeDenyStore{err: storageErr}, registry)
	if err := dbStore.SetPluginEnabled(ctx, nativeID, true); err != nil {
		t.Fatal(err)
	}
	allowed, err := failing.Allows(ctx, nativeID, "native-agent-b")
	if allowed || !errors.Is(err, storageErr) {
		t.Fatalf("storage failure cap = %v, %v; want false and storage error", allowed, err)
	}

	globalErr := errors.New("global storage unavailable")
	failingGlobal := plugin.NewNativePolicy(failingNativeGlobalStore{err: globalErr}, registry)
	allowed, err = failingGlobal.Allows(ctx, nativeID, "native-agent-b")
	if allowed || !errors.Is(err, globalErr) {
		t.Fatalf("global storage failure cap = %v, %v; want false and storage error", allowed, err)
	}

	if _, err := policy.Allows(ctx, "tool/unknown", "native-agent-b"); !errors.Is(err, plugin.ErrUnknownNativeID) {
		t.Fatalf("unknown native ID error = %v, want ErrUnknownNativeID", err)
	}
}
