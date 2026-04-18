package reflect

import (
	"context"
	"log/slog"
	"time"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

// expireDrafts deprecates all draft skills whose created-at timestamp in
// metadata is older than maxAge, using the DB-backed store.
func expireDrafts(store pkgplugins.SkillStore, maxAge time.Duration, log *slog.Logger) {
	if store == nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	if err := store.ExpireDrafts(context.Background(), cutoff); err != nil {
		log.Error("reflect: expire drafts", "error", err)
		return
	}
	log.Info("reflect: expired draft skills", "before", cutoff.Format(time.RFC3339))
}
