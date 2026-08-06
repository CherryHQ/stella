package groupingest

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/CherryHQ/stella/internal/memory"
	reflectpkg "github.com/CherryHQ/stella/internal/reflect"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const (
	defaultGroupFreshTokenBudget = 64_000
	defaultGroupPriorTokenBudget = 16_000
)

type ReviewUnitOptions struct {
	FreshTokenBudget int
	PriorTokenBudget int
}

type GroupSubjectCatalogEntry struct {
	Ref         string
	Subject     memory.GroupFactSubject
	SubjectID   string
	DisplayName string
}

// GroupReviewUnit is the bounded, public-only input for one Group Reflect
// window. Evidence stays runtime-only and is never copied into Group Facts.
type GroupReviewUnit struct {
	GroupID            string
	Text               string
	Subjects           map[string]GroupSubjectCatalogEntry
	ConsumedThroughSeq int64
	SkippedSeqs        []int64
	FreshCount         int
	PriorCount         int
	FreshTokens        int
	PriorTokens        int
}

type renderedReviewMessage struct {
	ActorType   string `json:"actor_type"`
	SubjectRef  string `json:"subject_ref"`
	DisplayName string `json:"display_name"`
	Content     string `json:"content"`
}

// BuildGroupReviewUnit packs fresh rows from oldest to newest and prior rows
// from newest backwards. A single oversized fresh event is skipped; aggregate
// overflow ends the current window before the first row that does not fit.
func BuildGroupReviewUnit(
	groupID string,
	priorRows []sqlc.CtxGroupMessage,
	freshRows []sqlc.CtxGroupMessage,
	opts ReviewUnitOptions,
) (GroupReviewUnit, error) {
	if groupID == "" {
		return GroupReviewUnit{}, fmt.Errorf("group_id is required")
	}
	if opts.FreshTokenBudget <= 0 {
		opts.FreshTokenBudget = defaultGroupFreshTokenBudget
	}
	if opts.PriorTokenBudget <= 0 {
		opts.PriorTokenBudget = defaultGroupPriorTokenBudget
	}

	fresh, consumedThrough, skipped := packFreshReviewRows(freshRows, opts.FreshTokenBudget)
	prior := packPriorReviewRows(priorRows, opts.PriorTokenBudget)
	subjects, refs := buildGroupSubjectCatalog(fresh, prior)

	unit := GroupReviewUnit{
		GroupID:            groupID,
		Subjects:           subjects,
		ConsumedThroughSeq: consumedThrough,
		SkippedSeqs:        skipped,
		FreshCount:         len(fresh),
		PriorCount:         len(prior),
		FreshTokens:        reviewRowsTokenCount(fresh),
		PriorTokens:        reviewRowsTokenCount(prior),
	}
	unit.Text = renderGroupReviewText(prior, fresh, refs)
	return unit, nil
}

func reviewRowsTokenCount(rows []sqlc.CtxGroupMessage) int {
	total := 0
	for _, row := range rows {
		total += memory.EstimateTokens(row.Content)
	}
	return total
}

func packFreshReviewRows(rows []sqlc.CtxGroupMessage, budget int) (packed []sqlc.CtxGroupMessage, consumedThrough int64, skipped []int64) {
	used := 0
	for _, row := range rows {
		tokens := memory.EstimateTokens(row.Content)
		if strings.TrimSpace(row.Content) == "" || tokens > budget {
			skipped = append(skipped, row.Seq)
			consumedThrough = row.Seq
			continue
		}
		if used+tokens > budget {
			break
		}
		packed = append(packed, row)
		used += tokens
		consumedThrough = row.Seq
	}
	return packed, consumedThrough, skipped
}

func packPriorReviewRows(rows []sqlc.CtxGroupMessage, budget int) []sqlc.CtxGroupMessage {
	used := 0
	start := len(rows)
	for i := len(rows) - 1; i >= 0; i-- {
		content := strings.TrimSpace(rows[i].Content)
		if content == "" {
			continue
		}
		tokens := memory.EstimateTokens(content)
		if tokens > budget || used+tokens > budget {
			break
		}
		used += tokens
		start = i
	}
	if start >= len(rows) {
		return nil
	}
	return append([]sqlc.CtxGroupMessage(nil), rows[start:]...)
}

func buildGroupSubjectCatalog(
	fresh []sqlc.CtxGroupMessage,
	prior []sqlc.CtxGroupMessage,
) (map[string]GroupSubjectCatalogEntry, map[string]string) {
	subjects := make(map[string]GroupSubjectCatalogEntry)
	refs := make(map[string]string)
	next := 1
	add := func(row sqlc.CtxGroupMessage) {
		subject := memory.GroupFactSubject(row.ActorType)
		if subject != memory.GroupFactSubjectHuman && subject != memory.GroupFactSubjectAgent {
			return
		}
		key := groupActorKey(subject, row.ActorID)
		displayName := row.ActorID
		if row.ActorDisplayName.Valid && row.ActorDisplayName.String != "" {
			displayName = row.ActorDisplayName.String
		}
		if ref, exists := refs[key]; exists {
			entry := subjects[ref]
			entry.DisplayName = displayName
			subjects[ref] = entry
			return
		}
		ref := fmt.Sprintf("subject-%04d", next)
		next++
		refs[key] = ref
		subjects[ref] = GroupSubjectCatalogEntry{
			Ref:         ref,
			Subject:     subject,
			SubjectID:   row.ActorID,
			DisplayName: displayName,
		}
	}
	for _, row := range prior {
		add(row)
	}
	for _, row := range fresh {
		add(row)
	}
	return subjects, refs
}

func renderGroupReviewText(
	prior []sqlc.CtxGroupMessage,
	fresh []sqlc.CtxGroupMessage,
	refs map[string]string,
) string {
	render := func(rows []sqlc.CtxGroupMessage) []renderedReviewMessage {
		out := make([]renderedReviewMessage, 0, len(rows))
		for _, row := range rows {
			subject := memory.GroupFactSubject(row.ActorType)
			ref := refs[groupActorKey(subject, row.ActorID)]
			displayName := row.ActorID
			if row.ActorDisplayName.Valid && row.ActorDisplayName.String != "" {
				displayName = row.ActorDisplayName.String
			}
			out = append(out, renderedReviewMessage{
				ActorType:   row.ActorType,
				SubjectRef:  ref,
				DisplayName: reflectpkg.RedactReviewText(displayName),
				Content:     reflectpkg.RedactReviewText(row.Content),
			})
		}
		return out
	}
	priorJSON, _ := json.Marshal(render(prior))
	freshJSON, _ := json.Marshal(render(fresh))

	var b strings.Builder
	b.WriteString("<prior_public_context>\n")
	b.Write(priorJSON)
	b.WriteString("\n</prior_public_context>\n")
	b.WriteString("<fresh_public_messages>\n")
	b.Write(freshJSON)
	b.WriteString("\n</fresh_public_messages>\n")
	return b.String()
}
