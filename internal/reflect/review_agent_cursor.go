package reflect

import (
	"context"

	"github.com/CherryHQ/stella/internal/platform/config"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

const reviewAgentCursorStateKey = "reflect_review_agent_cursor"

func (s *Service) orderAgentsForReview(ctx context.Context, agents []config.Agent) []config.Agent {
	ordered := append([]config.Agent(nil), agents...)
	if len(ordered) <= 1 || s.stateStore == nil {
		return ordered
	}

	nextAgentID, ok := s.readNextReviewAgentID(ctx)
	if !ok || nextAgentID == "" {
		return ordered
	}
	for i, agent := range ordered {
		if agent.ID != nextAgentID {
			continue
		}
		if i == 0 {
			return ordered
		}
		rotated := append([]config.Agent(nil), ordered[i:]...)
		rotated = append(rotated, ordered[:i]...)
		return rotated
	}
	return ordered
}

func (s *Service) readNextReviewAgentID(ctx context.Context) (string, bool) {
	value, ok, err := s.stateStore.Get(ctx, pkgplugins.StateScope{Kind: pkgplugins.StateScopeGlobal}, reviewAgentCursorStateKey)
	if err != nil {
		s.log.Warn("reflect: read review agent cursor failed", "error", err)
		return "", false
	}
	if !ok {
		return "", false
	}
	nextAgentID, _ := value["next_agent_id"].(string)
	return nextAgentID, nextAgentID != ""
}

func (s *Service) recordNextReviewAgentCursor(ctx context.Context, ordered []config.Agent, processed int) {
	if len(ordered) == 0 || s.stateStore == nil {
		return
	}
	nextID := nextReviewAgentID(ordered, processed)
	if nextID == "" {
		return
	}
	if err := s.stateStore.Set(ctx, pkgplugins.StateScope{Kind: pkgplugins.StateScopeGlobal}, reviewAgentCursorStateKey, map[string]any{
		"next_agent_id": nextID,
	}); err != nil {
		s.log.Warn("reflect: write review agent cursor failed", "error", err)
	}
}

func nextReviewAgentID(ordered []config.Agent, processed int) string {
	if len(ordered) == 0 {
		return ""
	}
	if processed < 0 {
		processed = 0
	}
	if processed < len(ordered) {
		return ordered[processed].ID
	}
	if len(ordered) == 1 {
		return ordered[0].ID
	}
	// A full pass rotates the next run by one agent so future soft-budgeted
	// runs do not always start from the same ListEnabledAgents head.
	return ordered[1].ID
}
