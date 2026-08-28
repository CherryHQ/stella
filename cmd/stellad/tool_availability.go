package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/vault"
)

type emailConfigMetaGetter interface {
	GetScopedMeta(ctx context.Context, scope string, userID string, agentID string, name string) (vault.EntryMeta, error)
}

type oauthProviderStatusGetter interface {
	AnyProviderConfigured(ctx context.Context, userID string) (bool, error)
}

// libraryToolAvailable accepts every authorized user-representing Agent run,
// independent of channel or session kind. Group runs stay excluded because
// they deliberately carry no trusted user identity.
func libraryToolAvailable(ctx context.Context, params agent.RunnerParams) (bool, error) {
	baseline, err := agent.BuiltinToolAvailable(ctx, params)
	if err != nil {
		return false, err
	}
	return params.GroupID == "" && baseline, nil
}

// emailToolAvailable gates the email tool on a stored EMAIL_CONFIG. A vault
// lookup that fails is reported, not guessed: the caller fails the runner build
// and retries, rather than caching a tool that can send mail on the user's
// behalf (or caching its absence) from one bad query.
func emailToolAvailable(v emailConfigMetaGetter) func(context.Context, agent.RunnerParams) (bool, error) {
	return func(ctx context.Context, params agent.RunnerParams) (bool, error) {
		baseline, err := agent.BuiltinToolAvailable(ctx, params)
		if err != nil || !baseline {
			return false, err
		}
		if v == nil {
			return true, nil
		}
		_, err = v.GetScopedMeta(ctx, vault.ScopeUser, params.UserID, "", "EMAIL_CONFIG")
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, pgx.ErrNoRows):
			return false, nil
		default:
			return false, fmt.Errorf("read email config: %w", err)
		}
	}
}

// oauthToolAvailable gates the oauth tool on at least one configured provider,
// and reports a failed status query for the same reason emailToolAvailable does.
func oauthToolAvailable(s oauthProviderStatusGetter) func(context.Context, agent.RunnerParams) (bool, error) {
	return func(ctx context.Context, params agent.RunnerParams) (bool, error) {
		baseline, err := agent.BuiltinToolAvailable(ctx, params)
		if err != nil || !baseline {
			return false, err
		}
		if s == nil {
			return true, nil
		}
		configured, err := s.AnyProviderConfigured(ctx, params.UserID)
		if err != nil {
			return false, fmt.Errorf("read oauth provider status: %w", err)
		}
		return configured, nil
	}
}
