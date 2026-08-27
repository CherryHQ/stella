package settings

import (
	"context"

	"github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
)

// DeferredAgentMutation lets setup wire a tool before the runtime pool and Home
// deletion lifecycle exist. The target is populated before setup returns.
type DeferredAgentMutation struct{ target *access.Management }

func NewDeferredAgentMutation() *DeferredAgentMutation          { return &DeferredAgentMutation{} }
func (d *DeferredAgentMutation) Bind(target *access.Management) { d.target = target }
func (d *DeferredAgentMutation) Create(ctx context.Context, a authz.Authority, c config.Agent) (config.Agent, error) {
	if d.target == nil {
		return config.Agent{}, ErrUnavailable
	}
	return d.target.Create(ctx, a, c)
}

func (d *DeferredAgentMutation) Update(ctx context.Context, a authz.Authority, c config.Agent) (config.Agent, error) {
	if d.target == nil {
		return config.Agent{}, ErrUnavailable
	}
	return d.target.Update(ctx, a, c)
}

func (d *DeferredAgentMutation) Delete(ctx context.Context, a authz.Authority, id string) error {
	if d.target == nil {
		return ErrUnavailable
	}
	return d.target.Delete(ctx, a, id)
}

func (d *DeferredAgentMutation) UpdateIfUnchanged(ctx context.Context, a authz.Authority, expected, candidate config.Agent) (config.Agent, error) {
	if d.target == nil {
		return config.Agent{}, ErrUnavailable
	}
	return d.target.UpdateIfUnchanged(ctx, a, expected, candidate)
}

func (d *DeferredAgentMutation) DeleteIfUnchanged(ctx context.Context, a authz.Authority, expected config.Agent) error {
	if d.target == nil {
		return ErrUnavailable
	}
	return d.target.DeleteIfUnchanged(ctx, a, expected)
}
