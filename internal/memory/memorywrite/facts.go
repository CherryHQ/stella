package memorywrite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const factsScope = "fact"

// CreateFact inserts an active fact, bumps the shared user-agent memory version,
// and records the fact state in ctx_agent_memory_changelog.
func CreateFact(ctx context.Context, db *pgxpool.Pool, q *sqlc.Queries, in memory.FactWrite) (memory.Fact, error) {
	return writeFact(ctx, db, q, factWritePlan{
		action: "create",
		write:  in,
	})
}

// ReplaceFact deprecates an existing fact and creates a new active fact that
// points at the old row through supersedes.
func ReplaceFact(ctx context.Context, db *pgxpool.Pool, q *sqlc.Queries, oldFactID string, in memory.FactWrite) (memory.Fact, error) {
	in.Supersedes = oldFactID
	return writeFact(ctx, db, q, factWritePlan{
		action:    "replace",
		oldFactID: oldFactID,
		write:     in,
	})
}

// SetSingletonFact creates or replaces the single active fact for subject=user
// or subject=agent under the same advisory lock used for the write itself.
func SetSingletonFact(ctx context.Context, db *pgxpool.Pool, q *sqlc.Queries, in memory.FactWrite) (memory.Fact, error) {
	if in.Subject == memory.FactSubjectWorld {
		return memory.Fact{}, fmt.Errorf("world facts are not singleton facts")
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		return memory.Fact{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockMemory(ctx, tx, in.UserID, in.AgentID); err != nil {
		return memory.Fact{}, err
	}

	qtx := q.WithTx(tx)
	active, err := qtx.ListActiveFactsBySubject(ctx, sqlc.ListActiveFactsBySubjectParams{
		UserID:  in.UserID,
		AgentID: in.AgentID,
		Subject: string(in.Subject),
	})
	if err != nil {
		return memory.Fact{}, fmt.Errorf("list singleton fact: %w", err)
	}
	if len(active) == 0 {
		return writeFactLocked(ctx, tx, qtx, factWritePlan{action: "create", write: in})
	}

	in.Supersedes = active[0].ID
	return writeFactLocked(ctx, tx, qtx, factWritePlan{
		action:    "replace",
		oldFactID: active[0].ID,
		write:     in,
	})
}

// ResetUserAgentMemory resets the user-agent memory surface: it deprecates all
// active fact-backed memories, then clears the legacy memory row columns
// (constraints and profile entries) in place. The row itself is kept so the
// version clock stays monotonic; deleting it would restart versions at 1 and
// let frozen sessions replay stale fact changelog state.
func ResetUserAgentMemory(ctx context.Context, db *pgxpool.Pool, q *sqlc.Queries, userID string, agentID string) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockMemory(ctx, tx, userID, agentID); err != nil {
		return err
	}

	qtx := q.WithTx(tx)
	beforeVersion, err := currentMemoryVersion(ctx, qtx, userID, agentID)
	if err != nil {
		return err
	}
	source := factSourceFromContext(ctx)

	type deprecatedFact struct {
		before memory.Fact
		after  memory.Fact
	}
	var deprecated []deprecatedFact
	for _, subject := range []memory.FactSubject{memory.FactSubjectUser, memory.FactSubjectAgent, memory.FactSubjectWorld} {
		active, err := qtx.ListActiveFactsBySubject(ctx, sqlc.ListActiveFactsBySubjectParams{
			UserID:  userID,
			AgentID: agentID,
			Subject: string(subject),
		})
		if err != nil {
			return fmt.Errorf("list reset facts: %w", err)
		}
		for _, row := range active {
			deprecatedRow, err := qtx.DeprecateFact(ctx, sqlc.DeprecateFactParams{
				ID:      row.ID,
				UserID:  userID,
				AgentID: agentID,
			})
			if err != nil {
				return fmt.Errorf("deprecate reset fact: %w", err)
			}
			deprecated = append(deprecated, deprecatedFact{
				before: factFromRow(row),
				after:  factFromRow(deprecatedRow),
			})
		}
	}

	memoryRow, err := qtx.BumpAgentMemoryVersion(ctx, sqlc.BumpAgentMemoryVersionParams{
		UserID:  userID,
		AgentID: agentID,
	})
	if err != nil {
		return fmt.Errorf("bump memory version: %w", err)
	}
	for _, fact := range deprecated {
		if _, err := writeFactChangelog(ctx, qtx, userID, agentID, "delete", source, beforeVersion, memoryRow.Version, fact.before, fact.after); err != nil {
			return err
		}
	}
	if err := qtx.ClearUserAgentMemory(ctx, sqlc.ClearUserAgentMemoryParams{
		UserID:  userID,
		AgentID: agentID,
	}); err != nil {
		return fmt.Errorf("clear user-agent memory: %w", err)
	}

	return tx.Commit(ctx)
}

type factWritePlan struct {
	action    string
	oldFactID string
	write     memory.FactWrite
}

func writeFact(ctx context.Context, db *pgxpool.Pool, q *sqlc.Queries, plan factWritePlan) (memory.Fact, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return memory.Fact{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockMemory(ctx, tx, plan.write.UserID, plan.write.AgentID); err != nil {
		return memory.Fact{}, err
	}

	return writeFactLocked(ctx, tx, q.WithTx(tx), plan)
}

func writeFactLocked(ctx context.Context, tx pgx.Tx, qtx *sqlc.Queries, plan factWritePlan) (memory.Fact, error) {
	source := plan.write.Source
	if source == "" {
		source = factSourceFromContext(ctx)
	}
	source = normalizeFactSource(source)

	beforeVersion, err := currentMemoryVersion(ctx, qtx, plan.write.UserID, plan.write.AgentID)
	if err != nil {
		return memory.Fact{}, err
	}

	var deprecatedOld *memory.Fact
	var oldBefore memory.Fact
	if plan.oldFactID != "" {
		oldRow, err := qtx.GetFact(ctx, sqlc.GetFactParams{
			ID:      plan.oldFactID,
			UserID:  plan.write.UserID,
			AgentID: plan.write.AgentID,
		})
		if err != nil {
			return memory.Fact{}, fmt.Errorf("read old fact: %w", err)
		}
		if oldRow.Subject != string(plan.write.Subject) {
			return memory.Fact{}, fmt.Errorf("old fact subject %q does not match %q", oldRow.Subject, plan.write.Subject)
		}
		oldBefore = factFromRow(oldRow)
		row, err := qtx.DeprecateFact(ctx, sqlc.DeprecateFactParams{
			ID:      plan.oldFactID,
			UserID:  plan.write.UserID,
			AgentID: plan.write.AgentID,
		})
		if err != nil {
			return memory.Fact{}, fmt.Errorf("deprecate old fact: %w", err)
		}
		f := factFromRow(row)
		deprecatedOld = &f
	}

	metadata := plan.write.Metadata
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}

	row, err := qtx.InsertFact(ctx, sqlc.InsertFactParams{
		ID:         uuid.Must(uuid.NewV7()).String(),
		Subject:    string(plan.write.Subject),
		UserID:     plan.write.UserID,
		AgentID:    plan.write.AgentID,
		Content:    plan.write.Content,
		Metadata:   metadata,
		Supersedes: textOrNull(plan.write.Supersedes),
		Source:     string(source),
	})
	if err != nil {
		return memory.Fact{}, fmt.Errorf("insert fact: %w", err)
	}

	memoryRow, err := qtx.BumpAgentMemoryVersion(ctx, sqlc.BumpAgentMemoryVersionParams{
		UserID:  plan.write.UserID,
		AgentID: plan.write.AgentID,
	})
	if err != nil {
		return memory.Fact{}, fmt.Errorf("bump memory version: %w", err)
	}

	fact := factFromRow(row)
	changelogAction := plan.action
	if deprecatedOld != nil {
		changelogAction = "replace"
		if _, err := writeFactChangelog(ctx, qtx, plan.write.UserID, plan.write.AgentID, "deprecate", source, beforeVersion, memoryRow.Version, oldBefore, *deprecatedOld); err != nil {
			return memory.Fact{}, err
		}
	}
	if _, err := writeFactChangelog(ctx, qtx, plan.write.UserID, plan.write.AgentID, changelogAction, source, beforeVersion, memoryRow.Version, memory.Fact{}, fact); err != nil {
		return memory.Fact{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return memory.Fact{}, fmt.Errorf("commit fact write: %w", err)
	}
	return fact, nil
}

func currentMemoryVersion(ctx context.Context, q *sqlc.Queries, userID string, agentID string) (int64, error) {
	oldMemory, err := q.GetUserAgentMemory(ctx, sqlc.GetUserAgentMemoryParams{
		UserID:  userID,
		AgentID: agentID,
	})
	if err == nil {
		return oldMemory.Version, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return 0, fmt.Errorf("read memory version: %w", err)
}

func writeFactChangelog(ctx context.Context, q *sqlc.Queries, userID string, agentID string, action string, source memory.ChangeSource, beforeVersion int64, afterVersion int64, before memory.Fact, after memory.Fact) (string, error) {
	var beforeText pgtype.Text
	if before.ID != "" {
		b, err := json.Marshal(before)
		if err != nil {
			return "", fmt.Errorf("marshal before fact: %w", err)
		}
		beforeText = pgtype.Text{String: string(b), Valid: true}
	}
	var afterText pgtype.Text
	if after.ID != "" {
		b, err := json.Marshal(after)
		if err != nil {
			return "", fmt.Errorf("marshal after fact: %w", err)
		}
		afterText = pgtype.Text{String: string(b), Valid: true}
	}
	id := uuid.Must(uuid.NewV7()).String()
	if err := q.InsertMemoryChangelog(ctx, sqlc.InsertMemoryChangelogParams{
		ID:                  id,
		UserID:              userID,
		AgentID:             agentID,
		EntityID:            pgtype.Text{String: after.ID, Valid: after.ID != ""},
		Scope:               factsScope,
		Action:              action,
		Source:              string(source),
		MemoryVersionBefore: pgtype.Int8{Int64: beforeVersion, Valid: true},
		MemoryVersionAfter:  pgtype.Int8{Int64: afterVersion, Valid: true},
		BeforeText:          beforeText,
		AfterText:           afterText,
	}); err != nil {
		return "", fmt.Errorf("write fact changelog: %w", err)
	}
	return id, nil
}

func factSourceFromContext(ctx context.Context) memory.ChangeSource {
	return normalizeFactSource(memory.ChangeSourceFromContext(ctx))
}

func normalizeFactSource(source memory.ChangeSource) memory.ChangeSource {
	if source == memory.SourceReflect {
		return memory.SourceReflect
	}
	return memory.SourceManual
}

// ListActiveFacts returns the current active facts for one subject.
func ListActiveFacts(ctx context.Context, q *sqlc.Queries, userID string, agentID string, subject memory.FactSubject) ([]memory.Fact, error) {
	rows, err := q.ListActiveFactsBySubject(ctx, sqlc.ListActiveFactsBySubjectParams{
		UserID:  userID,
		AgentID: agentID,
		Subject: string(subject),
	})
	if err != nil {
		return nil, fmt.Errorf("list active facts: %w", err)
	}
	out := make([]memory.Fact, 0, len(rows))
	for _, row := range rows {
		out = append(out, factFromRow(row))
	}
	return out, nil
}

// ListActiveFactsAt reconstructs active facts at a frozen memory version from
// fact changelog payloads.
func ListActiveFactsAt(ctx context.Context, q *sqlc.Queries, userID string, agentID string, subject memory.FactSubject, version int64) ([]memory.Fact, error) {
	if version <= 0 {
		return ListActiveFacts(ctx, q, userID, agentID, subject)
	}
	rows, err := q.ListFactChangelogUpToVersion(ctx, sqlc.ListFactChangelogUpToVersionParams{
		UserID:             userID,
		AgentID:            agentID,
		MemoryVersionAfter: pgtype.Int8{Int64: version, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("list fact changelog: %w", err)
	}
	byID := map[string]memory.Fact{}
	for _, row := range rows {
		if !row.AfterText.Valid {
			continue
		}
		var fact memory.Fact
		if err := json.Unmarshal([]byte(row.AfterText.String), &fact); err != nil {
			return nil, fmt.Errorf("parse fact changelog state: %w", err)
		}
		byID[fact.ID] = fact
	}
	out := make([]memory.Fact, 0, len(byID))
	for _, fact := range byID {
		if fact.Subject == subject && fact.Status == memory.FactStatusActive {
			out = append(out, fact)
		}
	}
	return out, nil
}

func factFromRow(row sqlc.Fact) memory.Fact {
	f := memory.Fact{
		ID:        row.ID,
		Subject:   memory.FactSubject(row.Subject),
		Scope:     row.Scope,
		UserID:    row.UserID,
		AgentID:   row.AgentID,
		Content:   row.Content,
		Status:    memory.FactStatus(row.Status),
		Metadata:  row.Metadata,
		Version:   row.Version,
		Source:    memory.ChangeSource(row.Source),
		CreatedAt: row.CreatedAt.UTC(),
		UpdatedAt: row.UpdatedAt.UTC(),
	}
	if row.Supersedes.Valid {
		f.Supersedes = row.Supersedes.String
	}
	return f
}

func textOrNull(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}
