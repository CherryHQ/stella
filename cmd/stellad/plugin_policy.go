package main

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/mcp"
	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/plugin/host"
	"github.com/CherryHQ/stella/internal/plugin/manifest"
	"github.com/CherryHQ/stella/internal/scheduler"
)

func pluginBackendPolicy(allowPrivate bool) plugin.BackendPolicy {
	mcpPolicy := mcp.NewMCPBackendPolicy(mcp.EndpointPolicy{AllowPrivate: allowPrivate})
	return plugin.BackendPolicy{
		Validate: func(ctx context.Context, def plugin.Definition, cfg plugin.Config, resets []string) error {
			switch def.Backend {
			case plugin.BackendCLI:
				return manifest.ValidatePayload(ctx, def, cfg, resets)
			case plugin.BackendMCP:
				return mcpPolicy.Validate(ctx, def, cfg, resets)
			case plugin.BackendGo:
				return host.ValidatePayload(ctx, def, cfg, resets)
			default:
				return plugin.ErrInvalidDefinition
			}
		},
		Transition: func(ctx context.Context, tx pgx.Tx, authority authz.Authority, kind plugin.MutationKind, def plugin.Definition, before, after *plugin.Config) error {
			switch def.Backend {
			case plugin.BackendMCP:
				return mcpPolicy.Transition(ctx, tx, authority, kind, def, before, after)
			case plugin.BackendCLI, plugin.BackendGo:
				return nil
			default:
				return plugin.ErrInvalidDefinition
			}
		},
	}
}

func pluginBackgroundGate(service *plugin.Service) scheduler.BackgroundCapabilityGate {
	return func(ctx context.Context, authority authz.Authority, agentID string, ids ...string) error {
		snapshot, err := service.ResolveSnapshot(ctx, authority, agentID)
		if err != nil {
			return err
		}
		for _, id := range ids {
			effective, err := snapshot.Resolve(id)
			if err != nil {
				return err
			}
			if !effective.IsEffectivelyEnabled || effective.ConfigID == "" {
				return fmt.Errorf("plugin %s unavailable: %w", id, authz.ErrForbidden)
			}
		}
		return nil
	}
}
