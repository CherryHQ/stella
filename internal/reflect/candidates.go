package reflect

import (
	"context"
	"sort"
	"time"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
)

// candidate represents a session that may need review.
type candidate struct {
	session    memory.Session
	lastActive time.Time // from SessionInfo — used as tiebreaker
	lastReview time.Time // zero if never reviewed
}

func (s *Service) listUnreviewed(ctx context.Context, sm memory.SessionManager, agentID string) ([]candidate, error) {
	sessions, err := listSessionInfoForReview(ctx, sm, agentID)
	if err != nil {
		return nil, err
	}

	candidates := make([]candidate, 0, len(sessions))
	for _, sess := range sessions {
		cand, ok := s.unreviewedCandidate(ctx, sess)
		if !ok {
			continue
		}
		candidates = append(candidates, cand)
	}

	sortCandidates(candidates)
	if len(candidates) > s.batch {
		candidates = candidates[:s.batch]
	}
	return candidates, nil
}

// listUnreviewedFromRegistry uses session.Registry.ListForReview to apply the
// default review policy (excludes delegate/task/scheduler sessions) and
// Registry.MemoryScope to build authorised memory.Session values.
func (s *Service) listUnreviewedFromRegistry(ctx context.Context, reg *session.Registry, agentID string) ([]candidate, error) {
	infos, err := reg.ListForReview(ctx, session.ReviewRequest{AgentID: agentID, Policy: session.DefaultReviewPolicy()})
	if err != nil {
		return nil, err
	}

	candidates := make([]candidate, 0, len(infos))
	for _, info := range infos {
		cand, ok := s.unreviewedCandidateFromRegistry(ctx, reg, info)
		if !ok {
			continue
		}
		candidates = append(candidates, cand)
	}

	sortCandidates(candidates)
	if len(candidates) > s.batch {
		candidates = candidates[:s.batch]
	}
	return candidates, nil
}

func listSessionInfoForReview(ctx context.Context, sm memory.SessionManager, agentID string) ([]memory.SessionInfo, error) {
	opts := memory.ListOptions{AgentID: agentID, IncludeArchived: false}
	if lister, ok := sm.(interface {
		ListInfoForReview(ctx context.Context, opts memory.ListOptions) ([]memory.SessionInfo, error)
	}); ok {
		return lister.ListInfoForReview(ctx, opts)
	}
	ctx = authz.WithAgentID(ctx, agentID)
	return sm.ListInfo(ctx, opts)
}

func (s *Service) unreviewedCandidateFromRegistry(ctx context.Context, reg *session.Registry, info session.Info) (candidate, bool) {
	if info.UserID == "" {
		return candidate{}, false
	}

	wm, err := s.wm.get(ctx, info.ID)
	if err != nil {
		s.log.Warn("reflect: watermark lookup failed, skipping session", "session", info.ID, "error", err)
		return candidate{}, false
	}
	if !info.LastActive.After(wm) {
		return candidate{}, false
	}

	return candidate{
		session:    reg.MemoryScope(info),
		lastActive: info.LastActive,
		lastReview: wm,
	}, true
}

func (s *Service) unreviewedCandidate(ctx context.Context, sess memory.SessionInfo) (candidate, bool) {
	if sess.UserID == "" {
		return candidate{}, false
	}

	wm, err := s.wm.get(ctx, sess.ID)
	if err != nil {
		s.log.Warn("reflect: watermark lookup failed, skipping session", "session", sess.ID, "error", err)
		return candidate{}, false
	}
	if !sess.LastActive.After(wm) {
		return candidate{}, false
	}

	return candidate{
		session: memory.Session{
			ID:      sess.ID,
			AgentID: sess.AgentID,
			UserID:  sess.UserID,
			Channel: sess.Channel,
		},
		lastActive: sess.LastActive,
		lastReview: wm,
	}, true
}

func sortCandidates(candidates []candidate) {
	sort.Slice(candidates, func(i, j int) bool {
		ri, rj := candidates[i].lastReview, candidates[j].lastReview
		if ri.Equal(rj) {
			return candidates[i].lastActive.Before(candidates[j].lastActive)
		}
		return ri.Before(rj)
	})
}
