package main

import (
	"context"
	"errors"

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

func emailToolAvailable(v emailConfigMetaGetter) func(context.Context, agent.RunnerParams) bool {
	return func(ctx context.Context, params agent.RunnerParams) bool {
		if !agent.DomainToolAvailable(params) {
			return false
		}
		if v == nil {
			return true
		}
		_, err := v.GetScopedMeta(ctx, vault.ScopeUser, params.UserID, "", "EMAIL_CONFIG")
		if err == nil {
			return true
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return false
		}
		return true
	}
}

func oauthToolAvailable(s oauthProviderStatusGetter) func(context.Context, agent.RunnerParams) bool {
	return func(ctx context.Context, params agent.RunnerParams) bool {
		if !agent.DomainToolAvailable(params) {
			return false
		}
		if s == nil {
			return true
		}
		configured, err := s.AnyProviderConfigured(ctx, params.UserID)
		if err != nil {
			return true
		}
		return configured
	}
}
