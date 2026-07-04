package main

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/vault"
)

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
