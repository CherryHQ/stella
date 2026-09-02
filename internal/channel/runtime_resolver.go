package channel

import (
	"context"

	"github.com/CherryHQ/stella/internal/platform/config"
)

// runtimeConfigStore is the minimal read surface the runtime resolver needs.
// config.Store satisfies it; the narrow shape is the point — a holder of a
// RuntimeResolver can resolve one channel or one agent name and nothing else
// (no listing, no mutation, no provider/plugin access).
type runtimeConfigStore interface {
	GetChannel(ctx context.Context, id string) (config.Channel, error)
	GetAgent(ctx context.Context, id string) (config.Agent, error)
}

// RuntimeResolver resolves the channel and agent that a runtime/ingress flow
// needs by ID. It replaces the aggregate config.Store that the webhook ingress
// and group member views used to hold: those flows are not the admin control
// plane, so they get this capability-restricted read port instead of a store
// they could list or mutate through.
type RuntimeResolver struct {
	store runtimeConfigStore
}

// NewRuntimeResolver builds the resolver over a channel/agent config reader.
func NewRuntimeResolver(store runtimeConfigStore) *RuntimeResolver {
	return &RuntimeResolver{store: store}
}

// Channel resolves one channel by ID for a runtime ingress lookup (e.g. the
// resource admission). The caller inspects the
// returned channel's type/enabled/agent fields.
func (r *RuntimeResolver) Channel(ctx context.Context, id string) (config.Channel, error) {
	return r.store.GetChannel(ctx, id)
}

// AgentName returns an agent's display name for a member/roster view. It is
// best-effort: a missing or unreadable agent yields ("", false) so a stale
// binding never breaks the surrounding view.
func (r *RuntimeResolver) AgentName(ctx context.Context, agentID string) (string, bool) {
	a, err := r.store.GetAgent(ctx, agentID)
	if err != nil {
		return "", false
	}
	return a.Name, true
}
