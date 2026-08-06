package lcm

import (
	"context"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// splitCompactionTail protects the six newest public group inputs and all
// subsequent private work. Legacy rows without origin metadata fall back to the
// ordinary user-turn policy; old data is intentionally not backfilled.
func splitCompactionTail(
	ctx context.Context,
	q *sqlc.Queries,
	convID string,
	items []sqlc.CtxItem,
	isGroup bool,
	freshTail int,
) (tail, older []sqlc.CtxItem, err error) {
	if !isGroup {
		tail, older = splitFreshTail(items, freshTail)
		return tail, older, nil
	}

	ordinals, err := q.ListRecentGroupOriginOrdinals(ctx, sqlc.ListRecentGroupOriginOrdinalsParams{
		ConversationID: convID,
		MaxCount:       groupLCMFreshTail,
	})
	if err != nil {
		return nil, nil, err
	}
	if len(ordinals) == 0 {
		tail, older = splitFreshTail(items, groupLCMFreshTail)
		return tail, older, nil
	}

	cutoff := ordinals[0]
	for _, ordinal := range ordinals[1:] {
		if ordinal < cutoff {
			cutoff = ordinal
		}
	}
	splitIdx := len(items)
	for i := range items {
		if items[i].Ordinal >= cutoff {
			splitIdx = i
			break
		}
	}
	splitIdx = correctToolPairSplit(items, splitIdx)
	return items[splitIdx:], items[:splitIdx], nil
}
