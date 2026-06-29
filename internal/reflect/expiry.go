package reflect

import (
	"context"
	"log/slog"
	"time"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// expireDrafts deprecates stale draft skills. Knowledge facts no longer live in
// the skills table, so their lifecycle belongs to the facts pipeline.
func expireDrafts(store pkgplugins.SkillStore, maxAge time.Duration, log *slog.Logger) {
	if store == nil {
		return
	}
	ctx := context.Background()

	cutoff := time.Now().Add(-maxAge)
	if err := store.ExpireDrafts(ctx, cutoff); err != nil {
		log.Error("reflect: expire skill drafts", "error", err)
	} else {
		log.Info("reflect: expired draft skills", "before", cutoff.Format(time.RFC3339))
	}
}
