package main

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/authz"
	agentaccess "github.com/CherryHQ/stella/internal/core/access"
	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/scheduler"
)

// pluginBackgroundGate is the shared admission gate for background Native
// capabilities. File resources have their own per-turn selection and do not
// pass through this Native policy.
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
func nativeAdministrativeCap(native *plugin.NativePolicy) func(context.Context, string, string) (bool, error) {
	return func(ctx context.Context, id, agentID string) (bool, error) {
		if native == nil {
			return false, plugin.ErrNativePolicyUnavailable
		}
		return native.Allows(ctx, id, agentID)
	}
}
