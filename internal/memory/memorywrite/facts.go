package memorywrite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const factsScope = "fact"

var (
	ErrFactNotRestorable    = errors.New("fact is not restorable")
	ErrFactRestoreBadCaller = errors.New("fact restore requires restored_by")
	ErrFactRestoreExpired   = errors.New("fact restore window expired")
	ErrFactDuplicateContent = errors.New("active knowledge already has this content")
)

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
	if active[0].Content == in.Content {
		fact := factFromRow(active[0])
		if err := tx.Commit(ctx); err != nil {
			return memory.Fact{}, fmt.Errorf("commit unchanged singleton fact read: %w", err)
		}
		return fact, nil
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
	constraintsBefore := "[]"
	memoryBefore, err := qtx.GetUserAgentMemory(ctx, sqlc.GetUserAgentMemoryParams{
		UserID:  userID,
		AgentID: agentID,
	})
	if err == nil {
		constraintsBefore = string(memoryBefore.Constraints)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("read memory before reset: %w", err)
	}

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
			before := factFromRow(row)
			if err := deleteKnowledgeUsageIfReflectWorld(ctx, qtx, before); err != nil {
				return err
			}
			deprecated = append(deprecated, deprecatedFact{
				before: before,
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
		if _, err := writeFactChangelog(ctx, qtx, userID, agentID, "deprecate", source, beforeVersion, memoryRow.Version, fact.before, fact.after); err != nil {
			return err
		}
	}
	if constraintsBefore != "[]" {
		id := uuid.Must(uuid.NewV7()).String()
		if err := qtx.InsertMemoryChangelog(ctx, sqlc.InsertMemoryChangelogParams{
			ID:                  id,
			UserID:              userID,
			AgentID:             agentID,
			Scope:               "constraint",
			Action:              "delete",
			Source:              string(source),
			MemoryVersionBefore: pgtype.Int8{Int64: beforeVersion, Valid: true},
			MemoryVersionAfter:  pgtype.Int8{Int64: memoryRow.Version, Valid: true},
			BeforeText:          pgtype.Text{String: constraintsBefore, Valid: true},
			AfterText:           pgtype.Text{String: "[]", Valid: true},
		}); err != nil {
			return fmt.Errorf("write reset constraint changelog: %w", err)
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

type RestoreCuratorDeprecatedKnowledgeFactInput struct {
	FactID     string
	UserID     string
	AgentID    string
	RestoredBy string
	Reason     string
}

type RestoreCuratorDeprecatedKnowledgeFactResult struct {
	Fact     memory.Fact
	Restored bool
}

// RestoreCuratorDeprecatedKnowledgeFact restores a Reflect-owned world fact that
// was deprecated by the usage curator. It intentionally refuses other deprecates:
// manual/semantic deprecations need a different review path.
func RestoreCuratorDeprecatedKnowledgeFact(ctx context.Context, db *pgxpool.Pool, q *sqlc.Queries, in RestoreCuratorDeprecatedKnowledgeFactInput) (RestoreCuratorDeprecatedKnowledgeFactResult, error) {
	if in.FactID == "" || in.UserID == "" || in.AgentID == "" {
		return RestoreCuratorDeprecatedKnowledgeFactResult{}, fmt.Errorf("fact restore: fact_id, user_id, and agent_id are required")
	}
	if in.RestoredBy == "" {
		return RestoreCuratorDeprecatedKnowledgeFactResult{}, ErrFactRestoreBadCaller
	}
	if db == nil || q == nil {
		return RestoreCuratorDeprecatedKnowledgeFactResult{}, fmt.Errorf("fact restore: db and sql queries are required")
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return RestoreCuratorDeprecatedKnowledgeFactResult{}, fmt.Errorf("begin fact restore: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockMemory(ctx, tx, in.UserID, in.AgentID); err != nil {
		return RestoreCuratorDeprecatedKnowledgeFactResult{}, err
	}
	qtx := q.WithTx(tx)

	beforeRow, err := qtx.GetFactForUpdate(ctx, sqlc.GetFactForUpdateParams{
		ID:      in.FactID,
		UserID:  in.UserID,
		AgentID: in.AgentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return RestoreCuratorDeprecatedKnowledgeFactResult{}, ErrFactNotRestorable
	}
	if err != nil {
		return RestoreCuratorDeprecatedKnowledgeFactResult{}, fmt.Errorf("lock fact for restore: %w", err)
	}
	before := factFromRow(beforeRow)
	if before.Scope != "user_agent" || before.Subject != memory.FactSubjectWorld || before.Source != memory.SourceReflect {
		return RestoreCuratorDeprecatedKnowledgeFactResult{}, ErrFactNotRestorable
	}
	if before.Status == memory.FactStatusActive {
		if err := tx.Commit(ctx); err != nil {
			return RestoreCuratorDeprecatedKnowledgeFactResult{}, fmt.Errorf("commit no-op fact restore: %w", err)
		}
		return RestoreCuratorDeprecatedKnowledgeFactResult{Fact: before, Restored: false}, nil
	}
	if before.Status != memory.FactStatusDeprecated {
		return RestoreCuratorDeprecatedKnowledgeFactResult{}, ErrFactNotRestorable
	}

	deprecateLog, err := qtx.GetLatestCuratorDeprecateFactChangelog(ctx, sqlc.GetLatestCuratorDeprecateFactChangelogParams{
		UserID:  in.UserID,
		AgentID: in.AgentID,
		FactID:  in.FactID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return RestoreCuratorDeprecatedKnowledgeFactResult{}, ErrFactNotRestorable
	}
	if err != nil {
		return RestoreCuratorDeprecatedKnowledgeFactResult{}, fmt.Errorf("read curator deprecate changelog: %w", err)
	}
	beforeVersion, err := currentMemoryVersion(ctx, qtx, in.UserID, in.AgentID)
	if err != nil {
		return RestoreCuratorDeprecatedKnowledgeFactResult{}, err
	}
	restoredRow, err := qtx.RestoreReflectWorldFact(ctx, sqlc.RestoreReflectWorldFactParams{
		ID:      in.FactID,
		UserID:  in.UserID,
		AgentID: in.AgentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return RestoreCuratorDeprecatedKnowledgeFactResult{}, ErrFactNotRestorable
	}
	if err != nil {
		return RestoreCuratorDeprecatedKnowledgeFactResult{}, fmt.Errorf("restore fact row: %w", err)
	}
	memoryRow, err := qtx.BumpAgentMemoryVersion(ctx, sqlc.BumpAgentMemoryVersionParams{
		UserID:  in.UserID,
		AgentID: in.AgentID,
	})
	if err != nil {
		return RestoreCuratorDeprecatedKnowledgeFactResult{}, fmt.Errorf("bump memory version: %w", err)
	}
	restored := factFromRow(restoredRow)
	if err := upsertKnowledgeUsageIfReflectWorld(ctx, qtx, restored); err != nil {
		return RestoreCuratorDeprecatedKnowledgeFactResult{}, err
	}
	metadata := restoreFactMetadata(in.RestoredBy, in.Reason, deprecateLog)
	if _, err := writeFactChangelogWithMetadata(ctx, qtx, in.UserID, in.AgentID, "restore", memory.SourceManual, beforeVersion, memoryRow.Version, before, restored, metadata); err != nil {
		return RestoreCuratorDeprecatedKnowledgeFactResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return RestoreCuratorDeprecatedKnowledgeFactResult{}, fmt.Errorf("commit fact restore: %w", err)
	}
	return RestoreCuratorDeprecatedKnowledgeFactResult{Fact: restored, Restored: true}, nil
}

type factWritePlan struct {
	action    string
	oldFactID string
	write     memory.FactWrite
}

type FactBatchAction string

const (
	FactBatchSetSingleton  FactBatchAction = "set_singleton"
	FactBatchCreate        FactBatchAction = "create"
	FactBatchReplaceMany   FactBatchAction = "replace_many"
	FactBatchDeprecateMany FactBatchAction = "deprecate_many"
)

// FactBatchOperation describes one transaction-scoped fact mutation. Reflect
// uses this helper so profile/soul/knowledge writes can fail closed as one fact
// line instead of committing partial writes.
type FactBatchOperation struct {
	Action        FactBatchAction
	Subject       memory.FactSubject
	Content       string
	Metadata      json.RawMessage
	TargetFactIDs []string
	// TargetUsageLastUsedAt optionally makes deprecate_many skip targets whose
	// Reflect knowledge usage changed since candidate selection. Curator uses
	// this to avoid retiring facts that were used while an armed run was pending.
	TargetUsageLastUsedAt map[string]time.Time
	// RequireEligibleActivityAfterUsage makes curator deprecation recheck that
	// the owning user-agent pair still has a review-eligible conversation after
	// the locked usage timestamp.
	RequireEligibleActivityAfterUsage bool
}

// ApplyFactBatch applies a batch of fact writes under one memory advisory lock
// and one database transaction. If any operation fails, all prior writes in the
// batch are rolled back.
func ApplyFactBatch(ctx context.Context, db *pgxpool.Pool, q *sqlc.Queries, userID string, agentID string, ops []FactBatchOperation) ([]memory.Fact, error) {
	if len(ops) == 0 {
		return nil, nil
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin fact batch: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockMemory(ctx, tx, userID, agentID); err != nil {
		return nil, err
	}

	qtx := q.WithTx(tx)
	written := make([]memory.Fact, 0, len(ops))
	for _, op := range ops {
		facts, err := applyFactBatchOperationLocked(ctx, qtx, userID, agentID, op)
		if err != nil {
			return nil, err
		}
		written = append(written, facts...)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit fact batch: %w", err)
	}
	return written, nil
}

func applyFactBatchOperationLocked(ctx context.Context, qtx *sqlc.Queries, userID string, agentID string, op FactBatchOperation) ([]memory.Fact, error) {
	write := memory.FactWrite{
		UserID:   userID,
		AgentID:  agentID,
		Subject:  op.Subject,
		Content:  op.Content,
		Metadata: op.Metadata,
		Source:   memory.SourceReflect,
	}
	switch op.Action {
	case FactBatchSetSingleton:
		if op.Subject == memory.FactSubjectWorld {
			return nil, fmt.Errorf("fact batch: world facts are not singleton facts")
		}
		if op.Content == "" {
			return nil, fmt.Errorf("fact batch: singleton content is required")
		}
		active, err := qtx.ListActiveFactsBySubject(ctx, sqlc.ListActiveFactsBySubjectParams{
			UserID:  userID,
			AgentID: agentID,
			Subject: string(op.Subject),
		})
		if err != nil {
			return nil, fmt.Errorf("fact batch: list singleton fact: %w", err)
		}
		if len(active) == 0 {
			fact, err := applyFactWriteLocked(ctx, qtx, factWritePlan{action: "create", write: write})
			return singleFactResult(fact, err)
		}
		if active[0].Content == op.Content {
			return []memory.Fact{factFromRow(active[0])}, nil
		}
		fact, err := applyFactWriteLocked(ctx, qtx, factWritePlan{action: "replace", oldFactID: active[0].ID, write: write})
		return singleFactResult(fact, err)
	case FactBatchCreate:
		if op.Content == "" {
			return nil, fmt.Errorf("fact batch: create content is required")
		}
		fact, err := applyFactWriteLocked(ctx, qtx, factWritePlan{action: "create", write: write})
		return singleFactResult(fact, err)
	case FactBatchReplaceMany:
		if len(op.TargetFactIDs) == 0 || op.Content == "" {
			return nil, fmt.Errorf("fact batch: replace_many requires targets and content")
		}
		for _, id := range op.TargetFactIDs {
			if _, err := applyDeprecateFactLocked(ctx, qtx, userID, agentID, op.Subject, id, nil); err != nil {
				return nil, err
			}
		}
		// facts.supersedes is scalar today, so record the first predecessor there
		// and keep the complete target set in metadata for later audit/reconcile.
		write.Supersedes = op.TargetFactIDs[0]
		write.Metadata = metadataWithReplacedFactIDs(op.Metadata, op.TargetFactIDs)
		fact, err := applyFactWriteLocked(ctx, qtx, factWritePlan{action: "replace", write: write})
		return singleFactResult(fact, err)
	case FactBatchDeprecateMany:
		if len(op.TargetFactIDs) == 0 || op.Content != "" {
			return nil, fmt.Errorf("fact batch: deprecate_many requires targets only")
		}
		deprecated := make([]memory.Fact, 0, len(op.TargetFactIDs))
		for _, id := range op.TargetFactIDs {
			shouldDeprecate, err := knowledgeUsageMatchesPrecondition(ctx, qtx, userID, agentID, id, op.TargetUsageLastUsedAt, op.RequireEligibleActivityAfterUsage)
			if err != nil {
				return nil, err
			}
			if !shouldDeprecate {
				continue
			}
			fact, err := applyDeprecateFactLocked(ctx, qtx, userID, agentID, op.Subject, id, op.Metadata)
			if err != nil {
				return nil, err
			}
			deprecated = append(deprecated, fact)
		}
		return deprecated, nil
	default:
		return nil, fmt.Errorf("fact batch: unknown action %q", op.Action)
	}
}

func knowledgeUsageMatchesPrecondition(ctx context.Context, qtx *sqlc.Queries, userID string, agentID string, factID string, expected map[string]time.Time, requireEligibleActivity bool) (bool, error) {
	expectedAt, ok := expected[factID]
	if !ok {
		return true, nil
	}
	row, err := qtx.GetKnowledgeUsageForUpdate(ctx, sqlc.GetKnowledgeUsageForUpdateParams{
		FactID:  factID,
		UserID:  userID,
		AgentID: agentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lock knowledge usage for fact %s: %w", factID, err)
	}
	if !row.LastUsedAt.Equal(expectedAt) {
		return false, nil
	}
	if !requireEligibleActivity {
		return true, nil
	}
	hasActivity, err := qtx.HasEligiblePairActivityAfter(ctx, sqlc.HasEligiblePairActivityAfterParams{
		UserID:  userID,
		AgentID: agentID,
		After:   row.LastUsedAt,
	})
	if err != nil {
		return false, fmt.Errorf("recheck eligible activity for fact %s: %w", factID, err)
	}
	return hasActivity, nil
}

func singleFactResult(fact memory.Fact, err error) ([]memory.Fact, error) {
	if err != nil {
		return nil, err
	}
	return []memory.Fact{fact}, nil
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
	fact, err := applyFactWriteLocked(ctx, qtx, plan)
	if err != nil {
		return memory.Fact{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return memory.Fact{}, fmt.Errorf("commit fact write: %w", err)
	}
	return fact, nil
}

func applyFactWriteLocked(ctx context.Context, qtx *sqlc.Queries, plan factWritePlan) (memory.Fact, error) {
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
		if err := deleteKnowledgeUsageIfReflectWorld(ctx, qtx, oldBefore); err != nil {
			return memory.Fact{}, err
		}
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
	if err := upsertKnowledgeUsageIfReflectWorld(ctx, qtx, fact); err != nil {
		return memory.Fact{}, err
	}
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

	return fact, nil
}

func applyDeprecateFactLocked(ctx context.Context, qtx *sqlc.Queries, userID string, agentID string, subject memory.FactSubject, factID string, changelogMetadata json.RawMessage) (memory.Fact, error) {
	source := factSourceFromContext(ctx)
	beforeVersion, err := currentMemoryVersion(ctx, qtx, userID, agentID)
	if err != nil {
		return memory.Fact{}, err
	}
	oldRow, err := qtx.GetFact(ctx, sqlc.GetFactParams{
		ID:      factID,
		UserID:  userID,
		AgentID: agentID,
	})
	if err != nil {
		return memory.Fact{}, fmt.Errorf("read fact to deprecate: %w", err)
	}
	oldBefore := factFromRow(oldRow)
	if oldBefore.Subject != subject {
		return memory.Fact{}, fmt.Errorf("fact %q subject %q does not match %q", factID, oldBefore.Subject, subject)
	}
	if oldBefore.Status != memory.FactStatusActive {
		return memory.Fact{}, fmt.Errorf("fact %q is not active", factID)
	}
	row, err := qtx.DeprecateFact(ctx, sqlc.DeprecateFactParams{
		ID:      factID,
		UserID:  userID,
		AgentID: agentID,
	})
	if err != nil {
		return memory.Fact{}, fmt.Errorf("deprecate fact: %w", err)
	}
	memoryRow, err := qtx.BumpAgentMemoryVersion(ctx, sqlc.BumpAgentMemoryVersionParams{
		UserID:  userID,
		AgentID: agentID,
	})
	if err != nil {
		return memory.Fact{}, fmt.Errorf("bump memory version: %w", err)
	}
	deprecated := factFromRow(row)
	if err := deleteKnowledgeUsageIfReflectWorld(ctx, qtx, oldBefore); err != nil {
		return memory.Fact{}, err
	}
	if _, err := writeFactChangelogWithMetadata(ctx, qtx, userID, agentID, "deprecate", source, beforeVersion, memoryRow.Version, oldBefore, deprecated, changelogMetadata); err != nil {
		return memory.Fact{}, err
	}
	return deprecated, nil
}

func upsertKnowledgeUsageIfReflectWorld(ctx context.Context, qtx *sqlc.Queries, fact memory.Fact) error {
	if fact.Subject != memory.FactSubjectWorld || fact.Source != memory.SourceReflect {
		return nil
	}
	if err := qtx.UpsertKnowledgeUsage(ctx, sqlc.UpsertKnowledgeUsageParams{
		FactID:  fact.ID,
		UserID:  fact.UserID,
		AgentID: fact.AgentID,
	}); err != nil {
		return fmt.Errorf("upsert knowledge usage: %w", err)
	}
	return nil
}

func deleteKnowledgeUsageIfReflectWorld(ctx context.Context, qtx *sqlc.Queries, fact memory.Fact) error {
	if fact.Subject != memory.FactSubjectWorld || fact.Source != memory.SourceReflect {
		return nil
	}
	if err := qtx.DeleteKnowledgeUsage(ctx, fact.ID); err != nil {
		return fmt.Errorf("delete knowledge usage: %w", err)
	}
	return nil
}

func metadataWithReplacedFactIDs(metadata json.RawMessage, targetIDs []string) json.RawMessage {
	payload := map[string]any{}
	if len(metadata) > 0 {
		_ = json.Unmarshal(metadata, &payload)
	}
	payload["replaced_fact_ids"] = targetIDs
	b, err := json.Marshal(payload)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

func restoreFactMetadata(restoredBy string, reason string, deprecated sqlc.CtxAgentMemoryChangelog) json.RawMessage {
	payload := map[string]any{
		"restored_by":                restoredBy,
		"deprecated_changelog_id":    deprecated.ID,
		"deprecated_at":              deprecated.CreatedAt.UTC().Format(time.RFC3339),
		"deprecated_changelog_scope": deprecated.Scope,
	}
	if reason != "" {
		payload["reason"] = reason
	}
	if deprecated.Metadata.Valid {
		deprecateMetadata := map[string]any{}
		if err := json.Unmarshal([]byte(deprecated.Metadata.String), &deprecateMetadata); err == nil {
			if rule, _ := deprecateMetadata["rule"].(string); rule != "" {
				payload["curator_rule"] = rule
			}
			if lastUsed, _ := deprecateMetadata["last_used_at"].(string); lastUsed != "" {
				payload["last_used_at"] = lastUsed
			}
		}
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
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
	return writeFactChangelogWithMetadata(ctx, q, userID, agentID, action, source, beforeVersion, afterVersion, before, after, nil)
}

func writeFactChangelogWithMetadata(ctx context.Context, q *sqlc.Queries, userID string, agentID string, action string, source memory.ChangeSource, beforeVersion int64, afterVersion int64, before memory.Fact, after memory.Fact, metadata json.RawMessage) (string, error) {
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
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
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
		Metadata:            pgtype.Text{String: string(metadata), Valid: true},
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
	facts, _, err := ListActiveFactsAtSnapshot(ctx, q, userID, agentID, subject, version)
	return facts, err
}

// ListActiveFactsAtSnapshot reconstructs active facts and reports whether any
// fact changelog existed for that subject. Callers use the flag to distinguish
// "facts prove this version is empty" from "this snapshot predates facts".
func ListActiveFactsAtSnapshot(ctx context.Context, q *sqlc.Queries, userID string, agentID string, subject memory.FactSubject, version int64) ([]memory.Fact, bool, error) {
	if version < 0 {
		return nil, false, fmt.Errorf("memory snapshot version must be non-negative: %d", version)
	}
	rows, err := q.ListFactChangelogUpToVersion(ctx, sqlc.ListFactChangelogUpToVersionParams{
		UserID:             userID,
		AgentID:            agentID,
		MemoryVersionAfter: pgtype.Int8{Int64: version, Valid: true},
	})
	if err != nil {
		return nil, false, fmt.Errorf("list fact changelog: %w", err)
	}
	byID := map[string]memory.Fact{}
	seenSubject := false
	for _, row := range rows {
		if row.BeforeText.Valid {
			var before memory.Fact
			if err := json.Unmarshal([]byte(row.BeforeText.String), &before); err != nil {
				return nil, false, fmt.Errorf("parse fact changelog before state: %w", err)
			}
			if before.Subject == subject {
				seenSubject = true
			}
		}
		if !row.AfterText.Valid {
			continue
		}
		var fact memory.Fact
		if err := json.Unmarshal([]byte(row.AfterText.String), &fact); err != nil {
			return nil, false, fmt.Errorf("parse fact changelog state: %w", err)
		}
		if fact.Subject == subject {
			seenSubject = true
		}
		byID[fact.ID] = fact
	}
	out := make([]memory.Fact, 0, len(byID))
	for _, fact := range byID {
		if fact.Subject == subject && fact.Status == memory.FactStatusActive {
			out = append(out, fact)
		}
	}
	return out, seenSubject, nil
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
