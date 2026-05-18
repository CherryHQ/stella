package reflect

import (
	"context"
	"log/slog"
	"time"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

const (
	defaultSkillDraftMaxAge   = 30 * 24 * time.Hour
	defaultContextDraftMaxAge = 30 * 24 * time.Hour
	defaultFactDraftMaxAge    = 90 * 24 * time.Hour
)

// expireDrafts deprecates stale draft entries:
//   - skills (disable_model_invocation=0): after maxAge (default 30 days)
//   - context knowledge: after 30 days
//   - fact knowledge: after 90 days
func expireDrafts(store pkgplugins.SkillStore, maxAge time.Duration, log *slog.Logger) {
	if store == nil {
		return
	}
	ctx := context.Background()

	// Expire regular skill drafts (knowledge entries excluded by query).
	cutoff := time.Now().Add(-maxAge)
	if err := store.ExpireDrafts(ctx, cutoff); err != nil {
		log.Error("reflect: expire skill drafts", "error", err)
	} else {
		log.Info("reflect: expired draft skills", "before", cutoff.Format(time.RFC3339))
	}

	// Expire knowledge drafts with type-aware cutoffs.
	if ks, ok := store.(pkgplugins.KnowledgeStore); ok {
		contextCutoff := time.Now().Add(-defaultContextDraftMaxAge)
		if err := ks.ExpireKnowledgeDraftsByType(ctx, pkgplugins.KnowledgeTypeContext, contextCutoff); err != nil {
			log.Error("reflect: expire context drafts", "error", err)
		} else {
			log.Info("reflect: expired draft context knowledge", "before", contextCutoff.Format(time.RFC3339))
		}

		factCutoff := time.Now().Add(-defaultFactDraftMaxAge)
		if err := ks.ExpireKnowledgeDraftsByType(ctx, pkgplugins.KnowledgeTypeFact, factCutoff); err != nil {
			log.Error("reflect: expire fact drafts", "error", err)
		} else {
			log.Info("reflect: expired draft fact knowledge", "before", factCutoff.Format(time.RFC3339))
		}
	}
}
