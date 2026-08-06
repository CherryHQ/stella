package lcm

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func (p *Provider) ListActiveGroupFacts(ctx context.Context, groupID string) ([]memory.GroupFact, error) {
	if groupID == "" {
		return nil, fmt.Errorf("group_id is required")
	}
	rows, err := p.q.ListActiveGroupFacts(ctx, groupID)
	if err != nil {
		return nil, fmt.Errorf("list active group facts: %w", err)
	}
	facts := make([]memory.GroupFact, 0, len(rows))
	for _, row := range rows {
		facts = append(facts, groupFactFromRow(row))
	}
	return facts, nil
}

func (p *Provider) GetGroupFactVersion(ctx context.Context, groupID string) (int64, error) {
	if groupID == "" {
		return 0, fmt.Errorf("group_id is required")
	}
	version, err := p.q.GetGroupMemoryVersion(ctx, groupID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get group fact version: %w", err)
	}
	return version, nil
}

func (p *Provider) ListGroupActorDisplayNames(ctx context.Context, groupID string) ([]memory.GroupActorDisplayName, error) {
	if groupID == "" {
		return nil, fmt.Errorf("group_id is required")
	}
	rows, err := p.q.ListLatestGroupActorDisplayNames(ctx, groupID)
	if err != nil {
		return nil, fmt.Errorf("list group actor display names: %w", err)
	}
	names := make([]memory.GroupActorDisplayName, 0, len(rows))
	for _, row := range rows {
		if !row.ActorDisplayName.Valid || row.ActorDisplayName.String == "" {
			continue
		}
		subject := memory.GroupFactSubject(row.ActorType)
		if subject != memory.GroupFactSubjectHuman && subject != memory.GroupFactSubjectAgent {
			continue
		}
		names = append(names, memory.GroupActorDisplayName{
			Subject:     subject,
			SubjectID:   row.ActorID,
			DisplayName: row.ActorDisplayName.String,
		})
	}
	return names, nil
}

func groupFactFromRow(row sqlc.CtxGroupFact) memory.GroupFact {
	return memory.GroupFact{
		ID:        row.ID,
		GroupID:   row.GroupID,
		Subject:   memory.GroupFactSubject(row.Subject),
		SubjectID: row.SubjectID.String,
		Content:   row.Content,
		Status:    memory.GroupFactStatus(row.Status),
		Source:    row.Source,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
}
