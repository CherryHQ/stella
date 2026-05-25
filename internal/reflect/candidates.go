package reflect

import (
	"context"
	"sort"
	"time"

	"github.com/CherryHQ/stella/internal/memory"
)

// candidate represents a session that may need review.
type candidate struct {
	session    memory.Session
	lastActive time.Time // from SessionInfo — used as tiebreaker
	lastReview time.Time // zero if never reviewed
}

func (s *Service) listUnreviewed(ctx context.Context, sm memory.SessionManager, agentID string) ([]candidate, error) {
	sessions, err := sm.ListInfo(ctx, memory.ListOptions{
		AgentID:         agentID,
		IncludeArchived: false,
	})
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
			OrgID:   sess.OrgID,
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
