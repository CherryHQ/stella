// Package groupingest consumes the group event log and extracts memories
// into group-shared and per-user drawers. It is the async counterpart of
// the synchronous single-chat memory path: messages are batched, sent to
// an Extractor (typically LLM-backed), and results are written via the
// existing memorywrite helpers.
package groupingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/memory/memorywrite"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const (
	PipelineMemoryIngest = "memory_ingest"
	defaultBatchSize     = 50
)

// ChatMessage is a single human message ready for extraction.
type ChatMessage struct {
	Seq       int64
	ActorID   string
	Content   string
	Timestamp string
}

// ExtractRequest is the input to an Extractor.
type ExtractRequest struct {
	GroupID            string
	Messages           []ChatMessage
	CurrentGroupMemory string
}

// ExtractResult is the output from an Extractor.
type ExtractResult struct {
	GroupMemory string              // updated group memory (empty = no change)
	UserFacts   map[string][]string // actor_id → extracted per-user facts
}

// Extractor extracts knowledge from a batch of group messages.
type Extractor interface {
	Extract(ctx context.Context, req ExtractRequest) (ExtractResult, error)
}

// UserWriter writes an extracted fact to a user's private memory.
// actorID is the platform sender ID from the event log; the implementation
// is responsible for resolving it to auth user + agent IDs.
type UserWriter func(ctx context.Context, actorID string, fact string) error

// Config configures an Ingester.
type Config struct {
	DB         *pgxpool.Pool
	Q          *sqlc.Queries
	Extractor  Extractor
	UserWriter UserWriter
	BatchSize  int
	Pipeline   string
}

// Ingester consumes group event logs and extracts memories.
type Ingester struct {
	db         *pgxpool.Pool
	q          *sqlc.Queries
	extractor  Extractor
	userWriter UserWriter
	batchSize  int64
	pipeline   string
	log        *slog.Logger

	mu      sync.Mutex
	running map[string]struct{}
}

// New creates an Ingester from the given config.
func New(cfg Config) *Ingester {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = defaultBatchSize
	}
	if cfg.Pipeline == "" {
		cfg.Pipeline = PipelineMemoryIngest
	}
	return &Ingester{
		db:         cfg.DB,
		q:          cfg.Q,
		extractor:  cfg.Extractor,
		userWriter: cfg.UserWriter,
		batchSize:  int64(cfg.BatchSize),
		pipeline:   cfg.Pipeline,
		log:        slog.With("component", "groupingest"),
		running:    make(map[string]struct{}),
	}
}

// RunOnce scans all groups with unprocessed messages and extracts memories.
// Per-group failures are logged and do not abort the scan; the first error
// is returned so the caller knows the run was not fully clean.
func (ing *Ingester) RunOnce(ctx context.Context) error {
	groups, err := ing.q.ListGroupsWithPendingIngest(ctx, ing.pipeline)
	if err != nil {
		return fmt.Errorf("list groups with pending ingest: %w", err)
	}

	var firstErr error
	for _, g := range groups {
		if err := ing.processGroup(ctx, g.GroupID, g.CursorSeq); err != nil {
			ing.log.Warn("group ingest failed", "group_id", g.GroupID, "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// ProcessGroup processes a single group's pending messages. Exported for
// targeted invocations (e.g. after a burst of messages in one group).
func (ing *Ingester) ProcessGroup(ctx context.Context, groupID string) error {
	cursor, err := ing.getCursorSeq(ctx, groupID)
	if err != nil {
		return err
	}
	return ing.processGroup(ctx, groupID, cursor)
}

func (ing *Ingester) getCursorSeq(ctx context.Context, groupID string) (int64, error) {
	row, err := ing.q.GetIngestCursor(ctx, sqlc.GetIngestCursorParams{
		GroupID:  groupID,
		Pipeline: ing.pipeline,
	})
	if err == nil {
		return row.LastSeq, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return 0, fmt.Errorf("get cursor: %w", err)
}

// tryLock acquires a per-group processing lock. Returns false if another
// goroutine is already processing this group.
func (ing *Ingester) tryLock(groupID string) bool {
	ing.mu.Lock()
	defer ing.mu.Unlock()
	if _, ok := ing.running[groupID]; ok {
		return false
	}
	ing.running[groupID] = struct{}{}
	return true
}

func (ing *Ingester) unlock(groupID string) {
	ing.mu.Lock()
	defer ing.mu.Unlock()
	delete(ing.running, groupID)
}

func (ing *Ingester) processGroup(ctx context.Context, groupID string, _ int64) error {
	if !ing.tryLock(groupID) {
		return nil
	}
	defer ing.unlock(groupID)

	// Re-read cursor under lock to avoid stale reads from concurrent callers.
	cursorSeq, err := ing.getCursorSeq(ctx, groupID)
	if err != nil {
		return err
	}

	messages, err := ing.q.ListGroupMessagesAfterSeq(ctx, sqlc.ListGroupMessagesAfterSeqParams{
		GroupID:    groupID,
		MinSeq:     cursorSeq,
		BatchLimit: int32(ing.batchSize),
	})
	if err != nil {
		return fmt.Errorf("list messages: %w", err)
	}
	if len(messages) == 0 {
		return nil
	}

	var maxSeq int64
	var humanMsgs []ChatMessage

	for _, m := range messages {
		if m.Seq > maxSeq {
			maxSeq = m.Seq
		}

		if m.ActorType == "agent" || m.ActorType == "system" {
			continue
		}

		if m.Content == "" {
			ing.deadLetter(ctx, groupID, m.Seq, "empty content")
			continue
		}

		ts := ""
		if m.PlatformTimestamp.Valid {
			ts = m.PlatformTimestamp.Time.UTC().Format(time.RFC3339Nano)
		}
		humanMsgs = append(humanMsgs, ChatMessage{
			Seq:       m.Seq,
			ActorID:   m.ActorID,
			Content:   m.Content,
			Timestamp: ts,
		})
	}

	if len(humanMsgs) == 0 {
		return ing.advanceCursor(ctx, groupID, maxSeq)
	}

	currentMemory, err := memorywrite.GetGroupMemory(ctx, ing.q, groupID)
	if err != nil {
		return fmt.Errorf("get current group memory: %w", err)
	}

	result, err := ing.extractor.Extract(ctx, ExtractRequest{
		GroupID:            groupID,
		Messages:           humanMsgs,
		CurrentGroupMemory: currentMemory,
	})
	if err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	if result.GroupMemory != "" {
		if err := memorywrite.SetGroupMemory(ctx, ing.db, ing.q, groupID, result.GroupMemory); err != nil {
			return fmt.Errorf("write group memory: %w", err)
		}
	}

	if ing.userWriter != nil {
		var userWriteErr error
		for actorID, facts := range result.UserFacts {
			for _, fact := range facts {
				if err := ing.userWriter(ctx, actorID, fact); err != nil {
					ing.log.Warn("write user fact failed", "actor_id", actorID, "error", err)
					userWriteErr = err
				}
			}
		}
		if userWriteErr != nil {
			return fmt.Errorf("user fact write failed, cursor not advanced: %w", userWriteErr)
		}
	}

	return ing.advanceCursor(ctx, groupID, maxSeq)
}

func (ing *Ingester) advanceCursor(ctx context.Context, groupID string, seq int64) error {
	return ing.q.UpsertIngestCursor(ctx, sqlc.UpsertIngestCursorParams{
		GroupID:  groupID,
		Pipeline: ing.pipeline,
		LastSeq:  seq,
	})
}

func (ing *Ingester) deadLetter(ctx context.Context, groupID string, seq int64, reason string) {
	if err := ing.q.CreateIngestError(ctx, sqlc.CreateIngestErrorParams{
		ID:       uuid.Must(uuid.NewV7()).String(),
		GroupID:  groupID,
		Pipeline: ing.pipeline,
		Seq:      seq,
		Reason:   reason,
	}); err != nil {
		ing.log.Warn("dead-letter insert failed", "group_id", groupID, "seq", seq, "error", err)
	}
}
