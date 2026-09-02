package access

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/core/providercred"
)

// fakeCreds records credential-write calls so tests can assert that encryption is
// never reached when authorization or canonical validation fails first.
type fakeCreds struct {
	listResult                            []providercred.Metadata
	listErr, setErr, deleteErr, createErr error
	setInputs                             []providercred.Input
	deletes                               []string
	created                               []config.Agent
	createInputs                          []providercred.Input
}

func (f *fakeCreds) List(context.Context, string) ([]providercred.Metadata, error) {
	return f.listResult, f.listErr
}

func (f *fakeCreds) Set(_ context.Context, _ string, input providercred.Input) (providercred.Metadata, error) {
	if f.setErr != nil {
		return providercred.Metadata{}, f.setErr
	}
	f.setInputs = append(f.setInputs, input)
	return providercred.Metadata{ProviderID: input.ProviderID, HasAPIKey: true}, nil
}

func (f *fakeCreds) Delete(_ context.Context, _, providerID string) error {
	f.deletes = append(f.deletes, providerID)
	return f.deleteErr
}

func (f *fakeCreds) CreateAgentWithCredentials(_ context.Context, a config.Agent, inputs []providercred.Input) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.created = append(f.created, a)
	f.createInputs = inputs
	return nil
}

type fakeProviders struct {
	ids []string
	err error
}

func (f fakeProviders) ListProviderIDs(context.Context) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]string(nil), f.ids...), nil
}

func newCredManagement(agents *fakeAgents, assign *fakeAssign, reloader AgentReloader, creds CredentialWriter, providers ProviderReader) *Management {
	pep := NewService(agents, assign)
	return NewManagement(pep, agents, assign, reloader, fakeUsers{}, nil, creds, providers, nil,
		WithAgentIDOccupancy(freeAgentIDOccupancy{}),
		WithOwnerDeletion(fakeOwnerDeletion{deleteAgent: func(ctx context.Context, id, _ string) error {
			return agents.DeleteAgent(ctx, id)
		}}))
}

func credAgent() *fakeAgents {
	return newFakeAgents(config.Agent{ID: "a", Name: "A", CreatorID: "u1", Scope: config.AgentScopeRestricted})
}

func openaiInput() providercred.Input {
	return providercred.Input{ProviderID: "openai", APIKey: "sk-agent"}
}

func TestSetProviderCredentialCreatorAllowedSyncsOnlyThatAgent(t *testing.T) {
	creds := &fakeCreds{}
	reloader := &fakeReloader{}
	m := newCredManagement(credAgent(), newFakeAssign(), reloader, creds, fakeProviders{ids: []string{"openai"}})

	meta, err := m.SetProviderCredential(context.Background(), userAuthority(t, "u1", false), "a", openaiInput())
	if err != nil {
		t.Fatalf("creator Set: %v", err)
	}
	if meta.ProviderID != "openai" || !meta.HasAPIKey {
		t.Fatalf("creator Set metadata = %+v", meta)
	}
	if len(creds.setInputs) != 1 {
		t.Fatalf("Set not delegated: %+v", creds.setInputs)
	}
	// Invalidation is scoped to exactly the mutated agent — no global reload port
	// exists on Management.
	if len(reloader.synced) != 1 || reloader.synced[0] != "a" {
		t.Fatalf("synced = %v, want exactly [a]", reloader.synced)
	}
}

func TestSetProviderCredentialAdminAllowed(t *testing.T) {
	creds := &fakeCreds{}
	m := newCredManagement(credAgent(), newFakeAssign(), &fakeReloader{}, creds, fakeProviders{ids: []string{"openai"}})
	if _, err := m.SetProviderCredential(context.Background(), userAuthority(t, "admin", true), "a", openaiInput()); err != nil {
		t.Fatalf("admin Set: %v", err)
	}
	if len(creds.setInputs) != 1 {
		t.Fatal("admin Set not delegated")
	}
}

func TestSetProviderCredentialAssignedNonCreatorDenied(t *testing.T) {
	creds := &fakeCreds{}
	assign := newFakeAssign()
	assign.byUser["u2"] = []string{"a"} // genuinely assigned, but not the creator
	m := newCredManagement(credAgent(), assign, &fakeReloader{}, creds, fakeProviders{ids: []string{"openai"}})

	_, err := m.SetProviderCredential(context.Background(), userAuthority(t, "u2", false), "a", openaiInput())
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("assigned non-creator Set = %v, want ErrForbidden", err)
	}
	if len(creds.setInputs) != 0 {
		t.Fatal("credential write reached despite denial")
	}
}

func TestSetProviderCredentialUnknownProviderRejectedBeforeWrite(t *testing.T) {
	creds := &fakeCreds{}
	m := newCredManagement(credAgent(), newFakeAssign(), &fakeReloader{}, creds, fakeProviders{ids: []string{"openai"}})

	// "openai-completions" is a type alias, not a canonical provider row id.
	_, err := m.SetProviderCredential(context.Background(), userAuthority(t, "u1", false), "a",
		providercred.Input{ProviderID: "openai-completions", APIKey: "sk"})
	if !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("alias Set = %v, want ErrUnknownProvider", err)
	}
	if len(creds.setInputs) != 0 {
		t.Fatal("encryption/write reached despite non-canonical provider")
	}
}

func TestSetProviderCredentialReloadFailureIsDurable(t *testing.T) {
	creds := &fakeCreds{}
	reloader := &fakeReloader{err: errors.New("pool down")}
	m := newCredManagement(credAgent(), newFakeAssign(), reloader, creds, fakeProviders{ids: []string{"openai"}})

	if _, err := m.SetProviderCredential(context.Background(), userAuthority(t, "u1", false), "a", openaiInput()); err != nil {
		t.Fatalf("Set must succeed despite reload failure: %v", err)
	}
	if len(creds.setInputs) != 1 {
		t.Fatal("durable write should have happened before the reload attempt")
	}
}

func TestDeleteProviderCredentialCreatorAllowedIdempotent(t *testing.T) {
	creds := &fakeCreds{}
	reloader := &fakeReloader{}
	m := newCredManagement(credAgent(), newFakeAssign(), reloader, creds, fakeProviders{ids: []string{"openai"}})

	if err := m.DeleteProviderCredential(context.Background(), userAuthority(t, "u1", false), "a", "openai"); err != nil {
		t.Fatalf("creator Delete: %v", err)
	}
	if len(creds.deletes) != 1 || reloader.synced[0] != "a" {
		t.Fatalf("delete=%v synced=%v", creds.deletes, reloader.synced)
	}
}

func TestDeleteProviderCredentialAssignedNonCreatorDenied(t *testing.T) {
	creds := &fakeCreds{}
	m := newCredManagement(credAgent(), newFakeAssign(), &fakeReloader{}, creds, fakeProviders{ids: []string{"openai"}})
	err := m.DeleteProviderCredential(context.Background(), userAuthority(t, "u2", false), "a", "openai")
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("assigned Delete = %v, want ErrForbidden", err)
	}
	if len(creds.deletes) != 0 {
		t.Fatal("delete reached despite denial")
	}
}

func TestListProviderCredentialsAuthorization(t *testing.T) {
	meta := []providercred.Metadata{{ProviderID: "openai", HasAPIKey: true}}
	newM := func() *Management {
		assign := newFakeAssign()
		assign.byUser["u1"] = []string{"a"}
		assign.byUser["u2"] = []string{"a"}
		return newCredManagement(credAgent(), assign, &fakeReloader{}, &fakeCreds{listResult: meta}, fakeProviders{ids: []string{"openai"}})
	}
	if got, err := newM().ListProviderCredentials(context.Background(), userAuthority(t, "u1", false), "a"); err != nil || len(got) != 1 {
		t.Fatalf("creator List = (%v, %v), want one meta", got, err)
	}
	if got, err := newM().ListProviderCredentials(context.Background(), userAuthority(t, "admin", true), "a"); err != nil || len(got) != 1 {
		t.Fatalf("admin List = (%v, %v)", got, err)
	}
	if got, err := newM().ListProviderCredentials(context.Background(), userAuthority(t, "u2", false), "a"); err != nil || len(got) != 1 {
		t.Fatalf("assigned List = (%v, %v), want one safe metadata row", got, err)
	}
}

func TestDeleteProviderCredentialRejectsAliasBeforeWrite(t *testing.T) {
	creds := &fakeCreds{}
	m := newCredManagement(credAgent(), newFakeAssign(), &fakeReloader{}, creds, fakeProviders{ids: []string{"openai"}})
	err := m.DeleteProviderCredential(context.Background(), userAuthority(t, "u1", false), "a", "openai-completions")
	if !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("alias Delete = %v, want ErrUnknownProvider", err)
	}
	if len(creds.deletes) != 0 {
		t.Fatal("delete reached despite non-canonical provider")
	}
}

func TestCreateWithProviderCredentialsSuccess(t *testing.T) {
	agents := newFakeAgents()
	assign := newFakeAssign()
	creds := &fakeCreds{}
	reloader := &fakeReloader{}
	m := newCredManagement(agents, assign, reloader, creds, fakeProviders{ids: []string{"openai"}})

	got, err := m.CreateWithProviderCredentials(context.Background(), userAuthority(t, "u1", false),
		config.Agent{ID: "newagent", Name: "New"}, []providercred.Input{openaiInput()})
	if err != nil {
		t.Fatalf("CreateWithProviderCredentials: %v", err)
	}
	if got.Scope != config.AgentScopeRestricted || got.CreatorID != "u1" {
		t.Fatalf("server-owned fields wrong: %+v", got)
	}
	if len(creds.created) != 1 || len(creds.createInputs) != 1 {
		t.Fatal("composite create not delegated with inputs")
	}
	if assign.assignCalls != 1 {
		t.Fatal("ordinary creator should be auto-assigned")
	}
	if len(reloader.synced) != 1 || reloader.synced[0] != "newagent" {
		t.Fatalf("synced = %v, want [newagent]", reloader.synced)
	}
}

func TestCreateWithProviderCredentialsAutoAssignCompensates(t *testing.T) {
	agents := newFakeAgents()
	assign := newFakeAssign()
	assign.assignErr = errors.New("assign down")
	creds := &fakeCreds{}
	m := newCredManagement(agents, assign, &fakeReloader{}, creds, fakeProviders{ids: []string{"openai"}})

	_, err := m.CreateWithProviderCredentials(context.Background(), userAuthority(t, "u1", false),
		config.Agent{ID: "newagent", Name: "New"}, []providercred.Input{openaiInput()})
	if err == nil {
		t.Fatal("expected failure when auto-assign fails")
	}
	// Compensation deletes the just-created agent; its FK cascade removes the
	// credential rows in production.
	if len(agents.deleted) != 1 || agents.deleted[0] != "newagent" {
		t.Fatalf("deleted = %v, want compensating delete of newagent", agents.deleted)
	}
}

func TestCreateWithProviderCredentialsRejectsUnknownProviderBeforeWrite(t *testing.T) {
	creds := &fakeCreds{}
	m := newCredManagement(newFakeAgents(), newFakeAssign(), &fakeReloader{}, creds, fakeProviders{ids: []string{"openai"}})

	_, err := m.CreateWithProviderCredentials(context.Background(), userAuthority(t, "u1", false),
		config.Agent{ID: "newagent", Name: "New"}, []providercred.Input{{ProviderID: "ghost", APIKey: "k"}})
	if !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("unknown provider create = %v, want ErrUnknownProvider", err)
	}
	if len(creds.created) != 0 {
		t.Fatal("agent+credentials persisted despite invalid provider")
	}
}

func TestCreateWithProviderCredentialsEmptyInputsFallsBackToPlainCreate(t *testing.T) {
	agents := newFakeAgents()
	creds := &fakeCreds{}
	m := newCredManagement(agents, newFakeAssign(), &fakeReloader{}, creds, fakeProviders{ids: []string{"openai"}})

	if _, err := m.CreateWithProviderCredentials(context.Background(), userAuthority(t, "admin", true),
		config.Agent{ID: "plain", Name: "Plain"}, nil); err != nil {
		t.Fatalf("empty-inputs create: %v", err)
	}
	if _, ok := agents.agents["plain"]; !ok {
		t.Fatal("plain agent should be created via the ordinary path")
	}
	if len(creds.created) != 0 {
		t.Fatal("composite path should not run with no inputs")
	}
}

func TestCredentialMethodsUnavailableWhenUnwired(t *testing.T) {
	// nil creds/providers: the ordinary Management (no credential support).
	m := newManagement(credAgent(), newFakeAssign(), &fakeReloader{}, fakeUsers{}, nil)
	ctx := context.Background()
	admin := userAuthority(t, "admin", true)
	if _, err := m.SetProviderCredential(ctx, admin, "a", openaiInput()); !errors.Is(err, ErrCredentialsUnavailable) {
		t.Fatalf("Set = %v, want ErrCredentialsUnavailable", err)
	}
	if err := m.DeleteProviderCredential(ctx, admin, "a", "openai"); !errors.Is(err, ErrCredentialsUnavailable) {
		t.Fatalf("Delete = %v, want ErrCredentialsUnavailable", err)
	}
	if _, err := m.ListProviderCredentials(ctx, admin, "a"); !errors.Is(err, ErrCredentialsUnavailable) {
		t.Fatalf("List = %v, want ErrCredentialsUnavailable", err)
	}
}

func TestCredentialCipherUnavailableMapsToManagementUnavailable(t *testing.T) {
	creds := &fakeCreds{setErr: providercred.ErrUnavailable}
	m := newCredManagement(credAgent(), newFakeAssign(), &fakeReloader{}, creds, fakeProviders{ids: []string{"openai"}})
	_, err := m.SetProviderCredential(context.Background(), userAuthority(t, "u1", false), "a", openaiInput())
	if !errors.Is(err, ErrCredentialsUnavailable) {
		t.Fatalf("Set = %v, want ErrCredentialsUnavailable", err)
	}
}
