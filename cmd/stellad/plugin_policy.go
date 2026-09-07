package main

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/authz"
	agentaccess "github.com/CherryHQ/stella/internal/core/access"
	"github.com/CherryHQ/stella/internal/mcp"
	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/plugin/manifest"
	"github.com/CherryHQ/stella/internal/scheduler"
	systemplugins "github.com/CherryHQ/stella/plugins/system"
)

func pluginBackendPolicy(allowPrivate bool) plugin.BackendPolicy {
	mcpPolicy := mcp.NewMCPBackendPolicy(mcp.EndpointPolicy{AllowPrivate: allowPrivate})
	return plugin.BackendPolicy{
		Validate: func(ctx context.Context, def plugin.Definition, cfg plugin.Config, resets []string) error {
			switch def.Backend {
			case plugin.BackendCLI:
				return validateCLIBackendPayload(ctx, def, cfg, resets)
			case plugin.BackendMCP:
				return mcpPolicy.Validate(ctx, def, cfg, resets)
			default:
				return plugin.ErrInvalidDefinition
			}
		},
		Transition: func(ctx context.Context, tx pgx.Tx, authority authz.Authority, kind plugin.MutationKind, def plugin.Definition, before, after *plugin.Config) error {
			switch def.Backend {
			case plugin.BackendMCP:
				return mcpPolicy.Transition(ctx, tx, authority, kind, def, before, after)
			case plugin.BackendCLI:
				return nil
			default:
				return plugin.ErrInvalidDefinition
			}
		},
	}
}

// validateCLIBackendPayload keeps core-owned commands out of every CLI CRUD
// path. The manifest validator remains responsible for the full payload
// contract; this composition check uses the same release declaration that the
// runtime and sandbox adapters consume.
func validateCLIBackendPayload(ctx context.Context, def plugin.Definition, cfg plugin.Config, resets []string) error {
	if err := manifest.ValidatePayload(ctx, def, cfg, resets); err != nil {
		return err
	}
	if manifest.IsSystemPlugin(def) {
		return nil
	}
	reserved := make(map[string]struct{}, len(systemplugins.RuntimeResources()))
	for _, resource := range systemplugins.RuntimeResources() {
		reserved[resource.Name] = struct{}{}
	}
	check := func(raw []byte, label string) error {
		if len(raw) == 0 {
			return nil
		}
		payload, err := manifest.DecodeCLIPayload(raw, label)
		if err != nil {
			return err
		}
		for _, binary := range payload.Binaries {
			if _, exists := reserved[binary.Name]; exists {
				return fmt.Errorf("%w: binary %q is reserved by the core runtime", plugin.ErrInvalidConfig, binary.Name)
			}
		}
		return nil
	}
	if err := check(def.Spec, "definition spec"); err != nil {
		return err
	}
	return check(cfg.Payload, "config payload")
}

func pluginBackgroundGate(native *plugin.NativePolicy, agents *agentaccess.Service) scheduler.BackgroundCapabilityGate {
	return func(ctx context.Context, authority authz.Authority, agentID string, ids ...string) error {
		if native == nil || agents == nil {
			return plugin.ErrNativePolicyUnavailable
		}
		if err := agents.Authorize(ctx, authority, agentID, authz.ActionExecute); err != nil {
			return err
		}
		for _, id := range ids {
			allowed, err := native.Allows(ctx, id, agentID)
			if err != nil {
				return err
			}
			if !allowed {
				return fmt.Errorf("plugin %s unavailable: %w", id, authz.ErrForbidden)
			}
		}
		return nil
	}
}

// nativeAdministrativeCap is the sole gate for Go-owned channel runtimes.
// Custom/manifest plugins have a separate Agent administrative gate and must
// never inherit native admission through an ID/name match.
func nativeAdministrativeCap(native *plugin.NativePolicy) func(context.Context, string, string) (bool, error) {
	return func(ctx context.Context, id, agentID string) (bool, error) {
		if native == nil {
			return false, plugin.ErrNativePolicyUnavailable
		}
		return native.Allows(ctx, id, agentID)
	}
}
