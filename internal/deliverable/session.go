package deliverable

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/session"
)

// RegistrySessionMinter mints a deliverable's worker (execution) session through
// the agent registry. The session is hidden — KindTask/ChannelTask — so it never
// appears in user-facing session lists or review candidates, matching the old
// tasks worker. It is called OUTSIDE every service tx (minting opens its own
// SQLite writer; running it inside the service tx would self-deadlock the single
// writer, as the old boot.go documents). Satisfies SessionMinter.
func RegistrySessionMinter(sm agent.ServiceManager) SessionMinter {
	return func(ctx context.Context, userID, agentID, projectID string) (string, error) {
		svc, err := resolveService(sm, userID, agentID)
		if err != nil {
			return "", err
		}
		info, err := svc.MintTaskSession(ctx, userID, agentID, projectID)
		if err != nil {
			return "", err
		}
		return info.ID, nil
	}
}

// RegistryPlanningSessionMinter mints the dedicated session a composite is
// decomposed in. Unlike worker sessions it uses KindDelegate/ChannelDelegate so
// the owning agent can resume it through the delegate tool and the user can
// re-open it from the UI to refine the decomposition by chatting (#525 planning
// sessions). Like the worker minter it runs OUTSIDE every service tx. Satisfies
// SessionMinter.
func RegistryPlanningSessionMinter(sm agent.ServiceManager) SessionMinter {
	return func(ctx context.Context, userID, agentID, projectID string) (string, error) {
		svc, err := resolveService(sm, userID, agentID)
		if err != nil {
			return "", err
		}
		info, err := svc.NewSession(ctx, userID, agentID, projectID, session.KindDelegate, session.ChannelDelegate)
		if err != nil {
			return "", err
		}
		return info.ID, nil
	}
}

// resolveService validates the owner identity and resolves the executor agent's
// Service from the registry. A missing user/agent or unknown agent is a caller
// error, not a panic — minting cannot proceed without an owner.
func resolveService(sm agent.ServiceManager, userID, agentID string) (*agent.Service, error) {
	if userID == "" {
		return nil, fmt.Errorf("deliverable has no user_id; cannot mint session")
	}
	if agentID == "" {
		return nil, fmt.Errorf("deliverable has no agent_id; cannot mint session")
	}
	svc := sm.GetService(agentID)
	if svc == nil {
		return nil, fmt.Errorf("no service for agent %q", agentID)
	}
	return svc, nil
}
