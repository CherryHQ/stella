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
			if got := libraryToolAvailable(context.Background(), test.params); got != test.want {
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

func TestEmailToolAvailableRequiresEmailConfigAndFailsOpen(t *testing.T) {
	params := agent.RunnerParams{UserID: "u1", AgentID: "a1"}
	if !emailToolAvailable(fakeEmailMetaGetter{})(context.Background(), params) {
		t.Fatal("EMAIL_CONFIG present should mount email tool")
	}
	if emailToolAvailable(fakeEmailMetaGetter{err: pgx.ErrNoRows})(context.Background(), params) {
		t.Fatal("missing EMAIL_CONFIG should skip email tool")
	}
	if !emailToolAvailable(fakeEmailMetaGetter{err: errors.New("db down")})(context.Background(), params) {
		t.Fatal("predicate errors should fail open")
	}
	if emailToolAvailable(fakeEmailMetaGetter{})(context.Background(), agent.RunnerParams{AgentID: "a1"}) {
		t.Fatal("builtin tool base predicate should still reject missing user")
	}
}

func TestOauthToolAvailableRequiresConfiguredProvider(t *testing.T) {
	params := agent.RunnerParams{UserID: "u1", AgentID: "a1"}
	if oauthToolAvailable(fakeOAuthStatuses{})(context.Background(), params) {
		t.Fatal("no configured providers should skip oauth tool")
	}
	if !oauthToolAvailable(fakeOAuthStatuses{configured: true})(context.Background(), params) {
		t.Fatal("configured provider should mount oauth tool")
	}
	if oauthToolAvailable(fakeOAuthStatuses{configured: true})(context.Background(), agent.RunnerParams{UserID: "u1"}) {
		t.Fatal("builtin tool base predicate should still reject missing agent")
	}
}
