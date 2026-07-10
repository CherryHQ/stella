package reflect

import (
	"context"
	"sort"
	"time"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
)

// reviewTarget represents a session that may need review.
type reviewTarget struct {
	session         memory.Session
	lastActive      time.Time // from SessionInfo — used as tiebreaker
	lastReview      time.Time // zero if never reviewed
	sourceGroupID   string
	privateOneToOne bool
}

func (s *Service) listUnreviewed(ctx context.Context, sm memory.SessionManager, agentID string) ([]reviewTarget, error) {
	sessions, err := listSessionInfoForReview(ctx, sm, agentID)
	if err != nil {
		return nil, err
	}

	targets := make([]reviewTarget, 0, len(sessions))
	for _, sess := range sessions {
		target, ok := s.unreviewedTarget(ctx, sess)
		if !ok {
			continue
		}
		targets = append(targets, target)
	}

	sortReviewTargets(targets)
	if limit := s.reviewTargetLimit(); limit > 0 && len(targets) > limit {
		targets = targets[:limit]
	}
	return targets, nil
}

// listUnreviewedFromRegistry uses session.Registry.ListForReview to apply the
// default review policy (excludes delegate/task/scheduler sessions) and
// Registry.MemoryScope to build authorised memory.Session values.
func (s *Service) listUnreviewedFromRegistry(ctx context.Context, reg *session.Registry, agentID string) ([]reviewTarget, error) {
	infos, err := reg.ListForReview(ctx, session.ReviewRequest{AgentID: agentID, Policy: session.DefaultReviewPolicy()})
	if err != nil {
		return nil, err
	}

	targets := make([]reviewTarget, 0, len(infos))
	for _, info := range infos {
		target, ok := s.unreviewedTargetFromRegistry(ctx, reg, info)
		if !ok {
			continue
		}
		targets = append(targets, target)
	}

	sortReviewTargets(targets)
	if limit := s.reviewTargetLimit(); limit > 0 && len(targets) > limit {
		targets = targets[:limit]
	}
	return targets, nil
}

func (s *Service) reviewTargetLimit() int {
	if s.maxReviewTargetsPerAgent > 0 {
		return s.maxReviewTargetsPerAgent
	}
	return defaultMaxReviewTargetsPerAgent
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

func (s *Service) unreviewedTargetFromRegistry(ctx context.Context, reg *session.Registry, info session.Info) (reviewTarget, bool) {
	if info.UserID == "" {
		return reviewTarget{}, false
	}

	wm, err := s.wm.get(ctx, info.ID)
	if err != nil {
		s.log.Warn("reflect: watermark lookup failed, skipping session", "session", info.ID, "error", err)
		return reviewTarget{}, false
	}
	if !info.LastActive.After(wm) {
		return reviewTarget{}, false
	}

	return reviewTarget{
		session:         reg.MemoryScope(info),
		lastActive:      info.LastActive,
		lastReview:      wm,
		sourceGroupID:   info.GroupID,
		privateOneToOne: info.GroupID == "" && info.UserID != "",
	}, true
}

func (s *Service) unreviewedTarget(ctx context.Context, sess memory.SessionInfo) (reviewTarget, bool) {
	if sess.UserID == "" {
		return reviewTarget{}, false
	}

	wm, err := s.wm.get(ctx, sess.ID)
	if err != nil {
		s.log.Warn("reflect: watermark lookup failed, skipping session", "session", sess.ID, "error", err)
		return reviewTarget{}, false
	}
	if !sess.LastActive.After(wm) {
		return reviewTarget{}, false
	}

	return reviewTarget{
		session: memory.Session{
			ID:      sess.ID,
			AgentID: sess.AgentID,
			UserID:  sess.UserID,
			Channel: sess.Channel,
			GroupID: sess.GroupID,
		},
		lastActive:      sess.LastActive,
		lastReview:      wm,
		sourceGroupID:   sess.GroupID,
		privateOneToOne: sess.GroupID == "" && sess.UserID != "",
	}, true
}

func sortReviewTargets(targets []reviewTarget) {
	sort.Slice(targets, func(i, j int) bool {
		ri, rj := targets[i].lastReview, targets[j].lastReview
		if ri.Equal(rj) {
			return targets[i].lastActive.Before(targets[j].lastActive)
		}
		return ri.Before(rj)
	})
}
