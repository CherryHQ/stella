package groupingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorywrite"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const (
	BuiltinGroupReflectJobName = "group-reflect-review"
	PipelineGroupReflect       = "group_reflect"
	defaultReviewQueryPageSize = 500
	defaultGroupWindowTimeout  = 8 * time.Minute
	defaultGroupRunSoftBudget  = 30 * time.Minute
)

type StructuredConfig struct {
	DB         *pgxpool.Pool
	Q          *sqlc.Queries
	FactStore  memory.GroupFactStore
	Reviewer   CandidateReviewer
	Reconciler ReconciliationRunner

	FreshTokenBudget int
	PriorTokenBudget int
	QueryPageSize    int
	WindowTimeout    time.Duration
	RunSoftBudget    time.Duration
	Now              func() time.Time
	Logger           *slog.Logger
}

// StructuredIngester owns the Group Reflect window loop. It keeps legacy group
// memory ingestion separate until the controlled mode cutover.
type StructuredIngester struct {
	db         *pgxpool.Pool
	q          *sqlc.Queries
	factStore  memory.GroupFactStore
	reviewer   CandidateReviewer
	reconciler ReconciliationRunner

	freshBudget int
	priorBudget int
	pageSize    int32
	windowTTL   time.Duration
	runBudget   time.Duration
	now         func() time.Time
	log         *slog.Logger

	mu      sync.Mutex
	running map[string]struct{}
}

func NewStructured(cfg StructuredConfig) (*StructuredIngester, error) {
	if cfg.DB == nil || cfg.Q == nil || cfg.FactStore == nil {
		return nil, fmt.Errorf("structured group reflect requires db, queries, and fact store")
	}
	if cfg.Reviewer.Stream == nil || cfg.Reconciler.Stream == nil {
		return nil, fmt.Errorf("structured group reflect requires candidate and reconciliation streams")
	}
	if cfg.FreshTokenBudget <= 0 {
		cfg.FreshTokenBudget = defaultGroupFreshTokenBudget
	}
	if cfg.PriorTokenBudget <= 0 {
		cfg.PriorTokenBudget = defaultGroupPriorTokenBudget
	}
	if cfg.QueryPageSize <= 0 {
		cfg.QueryPageSize = defaultReviewQueryPageSize
	}
	if cfg.WindowTimeout <= 0 {
		cfg.WindowTimeout = defaultGroupWindowTimeout
	}
	if cfg.RunSoftBudget <= 0 {
		cfg.RunSoftBudget = defaultGroupRunSoftBudget
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &StructuredIngester{
		db:          cfg.DB,
		q:           cfg.Q,
		factStore:   cfg.FactStore,
		reviewer:    cfg.Reviewer,
		reconciler:  cfg.Reconciler,
		freshBudget: cfg.FreshTokenBudget,
		priorBudget: cfg.PriorTokenBudget,
		pageSize:    int32(cfg.QueryPageSize),
		windowTTL:   cfg.WindowTimeout,
		runBudget:   cfg.RunSoftBudget,
		now:         cfg.Now,
		log:         cfg.Logger,
		running:     make(map[string]struct{}),
	}, nil
}

// NewStructuredForPool keeps raw query construction inside the owning domain
// while the daemon composition root only wires infrastructure dependencies.
func NewStructuredForPool(cfg StructuredConfig) (*StructuredIngester, error) {
	if cfg.DB == nil {
		return nil, fmt.Errorf("structured group reflect requires db")
	}
	cfg.Q = sqlc.New(cfg.DB)
	return NewStructured(cfg)
}

func (ing *StructuredIngester) RunOnce(ctx context.Context) error {
	startedAt := ing.now()
	ctx, span := startGroupReflectRunSpan(ctx)
	defer span.End()

	groups, err := ing.q.ListGroupsWithPendingIngest(ctx, PipelineGroupReflect)
	if err != nil {
		err = fmt.Errorf("list groups pending Group Reflect: %w", err)
		recordGroupReflectError(span, err)
		return err
	}
	canStart := func() bool { return ing.now().Sub(startedAt) < ing.runBudget }
	span.SetAttributes(attribute.Int("stella.group_reflect.pending_groups", len(groups)))

	var firstErr error
	processedGroups := 0
	failedGroups := 0
	softBudgetStop := false
	for _, group := range groups {
		if !canStart() {
			softBudgetStop = true
			break
		}
		processedGroups++
		stopped, err := ing.processGroup(ctx, group.GroupID, canStart)
		if stopped {
			softBudgetStop = true
		}
		if err != nil {
			failedGroups++
			ing.log.Warn("structured Group Reflect failed",
				"group_id", group.GroupID,
				"error", err,
			)
			if firstErr == nil {
				firstErr = err
			}
		}
		if stopped {
			break
		}
	}
	span.SetAttributes(
		attribute.Int("stella.group_reflect.processed_groups", processedGroups),
		attribute.Int("stella.group_reflect.failed_groups", failedGroups),
		attribute.Bool("stella.group_reflect.soft_budget_stop", softBudgetStop),
	)
	if firstErr != nil {
		recordGroupReflectError(span, firstErr)
	}
	ing.log.Info("structured Group Reflect run completed",
		"pending_groups", len(groups),
		"processed_groups", processedGroups,
		"failed_groups", failedGroups,
		"soft_budget_stop", softBudgetStop,
		"duration", time.Since(startedAt),
	)
	return firstErr
}

func (ing *StructuredIngester) ProcessGroup(ctx context.Context, groupID string) error {
	startedAt := ing.now()
	_, err := ing.processGroup(ctx, groupID, func() bool {
		return ing.now().Sub(startedAt) < ing.runBudget
	})
	return err
}

func (ing *StructuredIngester) processGroup(ctx context.Context, groupID string, canStart func() bool) (bool, error) {
	if groupID == "" {
		return false, fmt.Errorf("group_id is required")
	}
	if !ing.tryLock(groupID) {
		return false, nil
	}
	defer ing.unlock(groupID)

	for canStart() {
		windowCtx, cancel := context.WithTimeout(ctx, ing.windowTTL)
		cursor, err := ing.getGroupReflectCursor(windowCtx, groupID)
		if err != nil {
			cancel()
			return false, err
		}
		freshRows, err := ing.loadFreshRows(windowCtx, groupID, cursor)
		if err != nil {
			cancel()
			return false, err
		}
		if len(freshRows) == 0 {
			cancel()
			return false, nil
		}
		priorRows, err := ing.loadPriorRows(windowCtx, groupID, freshRows[0].Seq)
		if err != nil {
			cancel()
			return false, err
		}

		unit, err := BuildGroupReviewUnit(groupID, priorRows, freshRows, ReviewUnitOptions{
			FreshTokenBudget: ing.freshBudget,
			PriorTokenBudget: ing.priorBudget,
		})
		if err == nil {
			err = ing.processWindow(windowCtx, unit, freshRows)
		}
		cancel()
		if err != nil {
			return false, err
		}
	}
	return true, nil
}

func (ing *StructuredIngester) processWindow(
	ctx context.Context,
	unit GroupReviewUnit,
	freshRows []sqlc.CtxGroupMessage,
) (err error) {
	startedAt := time.Now()
	ctx, span := startGroupReflectWindowSpan(ctx, unit)
	defer func() {
		if err != nil {
			recordGroupReflectError(span, err)
		}
		span.End()
	}()

	if unit.ConsumedThroughSeq <= 0 {
		return fmt.Errorf("group Reflect window made no progress")
	}
	for _, seq := range unit.SkippedSeqs {
		reason := "single public event exceeds the Group Reflect fresh token budget"
		for _, row := range freshRows {
			if row.Seq == seq && strings.TrimSpace(row.Content) == "" {
				reason = "public event has empty content"
				break
			}
		}
		if err := ing.q.CreateIngestError(ctx, sqlc.CreateIngestErrorParams{
			ID:       uuid.Must(uuid.NewV7()).String(),
			GroupID:  unit.GroupID,
			Pipeline: PipelineGroupReflect,
			Seq:      seq,
			Reason:   reason,
		}); err != nil {
			return fmt.Errorf("record skipped Group Reflect event %d: %w", seq, err)
		}
	}

	review := GroupCandidateReviewResult{}
	if unit.FreshCount > 0 {
		review, err = ing.reviewer.Run(ctx, unit)
		if err != nil {
			return err
		}
	}
	span.SetAttributes(
		attribute.Int("stella.group_reflect.candidates_generated", len(review.Generated)),
		attribute.Int("stella.group_reflect.candidates_accepted", len(review.Accepted)),
		attribute.Int("stella.group_reflect.candidates_rejected", len(review.Gate.Rejected)),
		attribute.String("stella.group_reflect.candidate_rejection_reasons", groupGateRejectionSummary(review)),
	)
	writePlan := memory.GroupFactPlan{}
	relatedFacts := 0
	if len(review.Accepted) > 0 {
		facts, listErr := ing.factStore.ListActiveGroupFacts(ctx, unit.GroupID)
		if listErr != nil {
			return fmt.Errorf("list related Group Facts: %w", listErr)
		}
		relatedFacts = len(facts)
		bundle, buildErr := BuildGroupRelatedBundle(unit, review.Accepted, facts)
		if buildErr != nil {
			return buildErr
		}
		_, writePlan, err = ing.reconciler.Run(ctx, unit, bundle)
		if err != nil {
			return err
		}
	}
	writeResult, err := memorywrite.ApplyGroupFactPlan(ctx, ing.db, ing.q, memorywrite.ApplyGroupFactPlanRequest{
		GroupID:   unit.GroupID,
		Pipeline:  PipelineGroupReflect,
		Watermark: unit.ConsumedThroughSeq,
		Plan:      writePlan,
	})
	if err != nil {
		return err
	}
	var operationNoops, operationCreates, operationReplaces, operationDeprecates, operationTargets int
	for _, operation := range writePlan.Operations {
		operationTargets += len(operation.TargetFactIDs)
		switch operation.Action {
		case memory.GroupFactActionNoop:
			operationNoops++
		case memory.GroupFactActionCreate:
			operationCreates++
		case memory.GroupFactActionReplaceMany:
			operationReplaces++
		case memory.GroupFactActionDeprecateMany:
			operationDeprecates++
		}
	}
	span.SetAttributes(
		attribute.Int("stella.group_reflect.related_facts", relatedFacts),
		attribute.Int("stella.group_reflect.operations", len(writePlan.Operations)),
		attribute.Int("stella.group_reflect.operation_targets", operationTargets),
		attribute.Int("stella.group_reflect.operations_noop", operationNoops),
		attribute.Int("stella.group_reflect.operations_create", operationCreates),
		attribute.Int("stella.group_reflect.operations_replace", operationReplaces),
		attribute.Int("stella.group_reflect.operations_deprecate", operationDeprecates),
		attribute.Int("stella.group_reflect.changed_operations", writeResult.ChangedOperations),
		attribute.Int("stella.group_reflect.created_facts", len(writeResult.CreatedFactIDs)),
		attribute.Int64("stella.group_reflect.fact_version", writeResult.Version),
		attribute.Bool("stella.group_reflect.checkpoint_noop", writeResult.CheckpointNoop),
	)
	ing.log.Info("structured Group Reflect window completed",
		"group_id", unit.GroupID,
		"watermark", unit.ConsumedThroughSeq,
		"fresh_messages", unit.FreshCount,
		"prior_messages", unit.PriorCount,
		"fresh_tokens", unit.FreshTokens,
		"prior_tokens", unit.PriorTokens,
		"skipped_messages", len(unit.SkippedSeqs),
		"candidates_generated", len(review.Generated),
		"candidates_accepted", len(review.Accepted),
		"candidates_rejected", len(review.Gate.Rejected),
		"candidate_rejection_reasons", groupGateRejectionSummary(review),
		"related_facts", relatedFacts,
		"operations", len(writePlan.Operations),
		"operation_targets", operationTargets,
		"operations_noop", operationNoops,
		"operations_create", operationCreates,
		"operations_replace", operationReplaces,
		"operations_deprecate", operationDeprecates,
		"changed_operations", writeResult.ChangedOperations,
		"created_facts", len(writeResult.CreatedFactIDs),
		"fact_version", writeResult.Version,
		"duration", time.Since(startedAt),
	)
	return nil
}

func groupGateRejectionSummary(review GroupCandidateReviewResult) string {
	if len(review.Gate.Rejected) == 0 {
		return ""
	}
	counts := make(map[string]int)
	for _, decision := range review.Gate.Rejected {
		counts[string(decision.Reason)]++
	}
	reasons := make([]string, 0, len(counts))
	for reason := range counts {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	parts := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		parts = append(parts, fmt.Sprintf("%s=%d", reason, counts[reason]))
	}
	return strings.Join(parts, ",")
}

func (ing *StructuredIngester) getGroupReflectCursor(ctx context.Context, groupID string) (int64, error) {
	cursor, err := ing.q.GetIngestCursor(ctx, sqlc.GetIngestCursorParams{
		GroupID:  groupID,
		Pipeline: PipelineGroupReflect,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get Group Reflect cursor: %w", err)
	}
	return cursor.LastSeq, nil
}

func (ing *StructuredIngester) loadFreshRows(ctx context.Context, groupID string, cursor int64) ([]sqlc.CtxGroupMessage, error) {
	rows := make([]sqlc.CtxGroupMessage, 0, ing.pageSize)
	afterSeq := cursor
	used := 0
	for {
		page, err := ing.q.ListGroupMessagesAfterSeq(ctx, sqlc.ListGroupMessagesAfterSeqParams{
			GroupID:    groupID,
			MinSeq:     afterSeq,
			BatchLimit: ing.pageSize,
		})
		if err != nil {
			return nil, fmt.Errorf("load fresh Group Reflect messages: %w", err)
		}
		if len(page) == 0 {
			return rows, nil
		}
		for _, row := range page {
			rows = append(rows, row)
			tokens := memory.EstimateTokens(row.Content)
			if strings.TrimSpace(row.Content) == "" || tokens > ing.freshBudget {
				afterSeq = row.Seq
				continue
			}
			if used+tokens > ing.freshBudget {
				return rows, nil
			}
			used += tokens
			afterSeq = row.Seq
			if used >= ing.freshBudget {
				return rows, nil
			}
		}
		if len(page) < int(ing.pageSize) {
			return rows, nil
		}
	}
}

func (ing *StructuredIngester) loadPriorRows(ctx context.Context, groupID string, beforeSeq int64) ([]sqlc.CtxGroupMessage, error) {
	var descending []sqlc.CtxGroupMessage
	used := 0
	for {
		page, err := ing.q.ListRecentGroupMessagesBeforeSeq(ctx, sqlc.ListRecentGroupMessagesBeforeSeqParams{
			GroupID:   groupID,
			BeforeSeq: beforeSeq,
			MaxCount:  ing.pageSize,
		})
		if err != nil {
			return nil, fmt.Errorf("load prior Group Reflect messages: %w", err)
		}
		if len(page) == 0 {
			break
		}
		stop := false
		for _, row := range page {
			tokens := memory.EstimateTokens(row.Content)
			if strings.TrimSpace(row.Content) == "" {
				continue
			}
			if tokens > ing.priorBudget || used+tokens > ing.priorBudget {
				stop = true
				break
			}
			descending = append(descending, row)
			used += tokens
		}
		if stop || used >= ing.priorBudget || len(page) < int(ing.pageSize) {
			break
		}
		beforeSeq = page[len(page)-1].Seq
	}
	for left, right := 0, len(descending)-1; left < right; left, right = left+1, right-1 {
		descending[left], descending[right] = descending[right], descending[left]
	}
	return descending, nil
}

func (ing *StructuredIngester) tryLock(groupID string) bool {
	ing.mu.Lock()
	defer ing.mu.Unlock()
	if _, exists := ing.running[groupID]; exists {
		return false
	}
	ing.running[groupID] = struct{}{}
	return true
}

func (ing *StructuredIngester) unlock(groupID string) {
	ing.mu.Lock()
	delete(ing.running, groupID)
	ing.mu.Unlock()
}
