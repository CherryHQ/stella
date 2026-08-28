package main

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/vault"
)

func TestLibraryToolAvailable(t *testing.T) {
	tests := []struct {
		name   string
		params agent.RunnerParams
		want   bool
	}{
		{name: "trusted user Agent run", params: agent.RunnerParams{UserID: "u1", AgentID: "a1"}, want: true},
		{name: "group run", params: agent.RunnerParams{GroupID: "g1", AgentID: "a1"}, want: false},
		{name: "missing user", params: agent.RunnerParams{AgentID: "a1"}, want: false},
		{name: "missing Agent", params: agent.RunnerParams{UserID: "u1"}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := libraryToolAvailable(context.Background(), test.params)
			if err != nil {
				t.Fatalf("libraryToolAvailable: %v", err)
			}
			if got != test.want {
				t.Fatalf("libraryToolAvailable = %v, want %v", got, test.want)
			}
		})
	}
}

type fakeEmailMetaGetter struct{ err error }

func (f fakeEmailMetaGetter) GetScopedMeta(context.Context, string, string, string, string) (vault.EntryMeta, error) {
	return vault.EntryMeta{}, f.err
}

type fakeOAuthStatuses struct {
	configured bool
	err        error
}

func (f fakeOAuthStatuses) AnyProviderConfigured(context.Context, string) (bool, error) {
	return f.configured, f.err
}

func TestEmailToolAvailableRequiresEmailConfig(t *testing.T) {
	params := agent.RunnerParams{UserID: "u1", AgentID: "a1"}
	available, err := emailToolAvailable(fakeEmailMetaGetter{})(context.Background(), params)
	if err != nil || !available {
		t.Fatalf("EMAIL_CONFIG present should mount email tool: available=%v err=%v", available, err)
	}
	available, err = emailToolAvailable(fakeEmailMetaGetter{err: pgx.ErrNoRows})(context.Background(), params)
	if err != nil || available {
		t.Fatalf("missing EMAIL_CONFIG should skip email tool: available=%v err=%v", available, err)
	}
	available, err = emailToolAvailable(fakeEmailMetaGetter{})(context.Background(), agent.RunnerParams{AgentID: "a1"})
	if err != nil || available {
		t.Fatalf("builtin tool base predicate should still reject missing user: available=%v err=%v", available, err)
	}
}

// A vault outage must not be answered with a guess in either direction: the
// runner build fails and the caller retries.
func TestEmailToolAvailableReportsLookupFailure(t *testing.T) {
	params := agent.RunnerParams{UserID: "u1", AgentID: "a1"}
	lookupErr := errors.New("db down")
	available, err := emailToolAvailable(fakeEmailMetaGetter{err: lookupErr})(context.Background(), params)
	if err == nil {
		t.Fatal("vault lookup failure should be reported, not defaulted")
	}
	if !errors.Is(err, lookupErr) {
		t.Fatalf("error should wrap the lookup failure, got %v", err)
	}
	if available {
		t.Fatal("availability must be false when unknown")
	}
}

func TestOauthToolAvailableRequiresConfiguredProvider(t *testing.T) {
	params := agent.RunnerParams{UserID: "u1", AgentID: "a1"}
	available, err := oauthToolAvailable(fakeOAuthStatuses{})(context.Background(), params)
	if err != nil || available {
		t.Fatalf("no configured providers should skip oauth tool: available=%v err=%v", available, err)
	}
	available, err = oauthToolAvailable(fakeOAuthStatuses{configured: true})(context.Background(), params)
	if err != nil || !available {
		t.Fatalf("configured provider should mount oauth tool: available=%v err=%v", available, err)
	}
	available, err = oauthToolAvailable(fakeOAuthStatuses{configured: true})(context.Background(), agent.RunnerParams{UserID: "u1"})
	if err != nil || available {
		t.Fatalf("builtin tool base predicate should still reject missing agent: available=%v err=%v", available, err)
	}
}

func TestOauthToolAvailableReportsStatusFailure(t *testing.T) {
	statusErr := errors.New("db down")
	available, err := oauthToolAvailable(fakeOAuthStatuses{configured: true, err: statusErr})(context.Background(), agent.RunnerParams{UserID: "u1", AgentID: "a1"})
	if err == nil {
		t.Fatal("provider status failure should be reported, not defaulted")
	}
	if !errors.Is(err, statusErr) {
		t.Fatalf("error should wrap the status failure, got %v", err)
	}
	if available {
		t.Fatal("availability must be false when unknown")
	}
}
