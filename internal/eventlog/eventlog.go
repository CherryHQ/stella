// Package eventlog owns the authoritative, deduplicated group-message log
// (ctx_group_message) and its per-group ordering registry (ctx_group_state).
//
// Several bots may deliver the same human message; AppendGroupMessage collapses
// those into a single row and never silently drops one. It is the only sanctioned
// way to append — callers must not run raw INSERTs, or the dedup/seq invariants
// break.
package eventlog

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// ActorType records who spoke. It is a hard schema fact, never guessed from
// content: downstream (arbiter, memory ingest) acts on human rows only.
type ActorType string

const (
	ActorHuman ActorType = "human"
	ActorAgent ActorType = "agent"
)

// Message is one observed delivery to append. Content is the already-serialized
// payload (JSON []ai.ContentBlock by convention); eventlog treats it as opaque
// and stores it verbatim.
type Message struct {
	// Physical group identity (D0). The triple resolves to one registry row.
	Platform         string
	PlatformGroupID  string
	PlatformThreadID string // "" when the group has no sub-thread/topic

	SourceChannelID string // observing bot; audit only, never a dedup/route key

	ActorType ActorType
	ActorID   string // platform sender id (human) or agent id (agent)

	PlatformMessageID string // "" when the adapter cannot supply one
	ReplyTo           string // platform message id this replies to; "" if none

	// PlatformTimestamp is the platform-reported send time. Pass a non-zero,
	// dedup-grade (high-precision) time to enable the fallback idempotency key
	// when PlatformMessageID is absent; pass the zero value to skip it. Never
	// pass a local receive time — that would misclassify back-to-back identical
	// messages as redeliveries and drop data.
	PlatformTimestamp time.Time

	Content string
}

// AppendResult reports the outcome of an append.
type AppendResult struct {
	GroupID  string // surrogate id of the resolved registry row
	Seq      int64  // group-monotonic ordering token of the (existing or new) row
	Inserted bool   // false = an idempotent redelivery collapsed onto an existing row
	Message  sqlc.CtxGroupMessage
}

// Store appends to the group event log.
type Store struct {
	db *sql.DB
}

// NewStore returns a Store backed by the given database handle.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// AppendGroupMessage is the single sanctioned append primitive. It runs the
// closed, idempotent algorithm under BEGIN IMMEDIATE (SQLite is single-writer,
// so this serializes group writes): resolve-or-create the registry row, check
// for an existing message by unique key, and only on a miss bump next_seq and
// insert. An idempotent redelivery neither inserts a row nor consumes a seq.
func (s *Store) AppendGroupMessage(ctx context.Context, msg Message) (AppendResult, error) {
	if err := validate(msg); err != nil {
		return AppendResult{}, err
	}

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return AppendResult{}, fmt.Errorf("eventlog: acquire conn: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return AppendResult{}, fmt.Errorf("eventlog: begin immediate: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
	}()

	q := sqlc.New(conn)

	groupID, err := resolveGroupID(ctx, q, msg.Platform, msg.PlatformGroupID, msg.PlatformThreadID)
	if err != nil {
		return AppendResult{}, err
	}

	idemKey := idempotencyKey(groupID, msg)
	if existing, found, err := lookup(ctx, q, groupID, msg, idemKey); err != nil {
		return AppendResult{}, err
	} else if found {
		if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
			return AppendResult{}, fmt.Errorf("eventlog: commit: %w", err)
		}
		committed = true
		return AppendResult{GroupID: groupID, Seq: existing.Seq, Inserted: false, Message: existing}, nil
	}

	seq, err := q.BumpGroupSeq(ctx, groupID)
	if err != nil {
		return AppendResult{}, fmt.Errorf("eventlog: bump seq: %w", err)
	}

	row, err := q.CreateGroupMessage(ctx, sqlc.CreateGroupMessageParams{
		ID:                uuid.NewString(),
		GroupID:           groupID,
		Seq:               seq,
		SourceChannelID:   nullString(msg.SourceChannelID),
		ActorType:         string(msg.ActorType),
		ActorID:           msg.ActorID,
		PlatformMessageID: nullString(msg.PlatformMessageID),
		ReplyTo:           nullString(msg.ReplyTo),
		PlatformTimestamp: nullString(platformTimestamp(msg.PlatformTimestamp)),
		IdempotencyKey:    idemKey,
		Content:           msg.Content,
	})
	if err != nil {
		return AppendResult{}, fmt.Errorf("eventlog: create message: %w", err)
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return AppendResult{}, fmt.Errorf("eventlog: commit: %w", err)
	}
	committed = true
	return AppendResult{GroupID: groupID, Seq: seq, Inserted: true, Message: row}, nil
}

func validate(msg Message) error {
	if msg.Platform == "" || msg.PlatformGroupID == "" {
		return errors.New("eventlog: platform and platform_group_id are required")
	}
	if msg.ActorType != ActorHuman && msg.ActorType != ActorAgent {
		return fmt.Errorf("eventlog: invalid actor_type %q", msg.ActorType)
	}
	if msg.ActorID == "" {
		return errors.New("eventlog: actor_id is required")
	}
	return nil
}

// ResolveGroupID performs a get-or-create on the group registry for the given
// physical (platform, group, thread) triple and returns the surrogate group_id.
// It runs under BEGIN IMMEDIATE so the upsert is atomic.
func (s *Store) ResolveGroupID(ctx context.Context, platform, platformGroupID, platformThreadID string) (string, error) {
	if platform == "" || platformGroupID == "" {
		return "", errors.New("eventlog: platform and platform_group_id are required")
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return "", fmt.Errorf("eventlog: acquire conn: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return "", fmt.Errorf("eventlog: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
	}()

	q := sqlc.New(conn)
	id, err := resolveGroupID(ctx, q, platform, platformGroupID, platformThreadID)
	if err != nil {
		return "", err
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return "", fmt.Errorf("eventlog: commit: %w", err)
	}
	committed = true
	return id, nil
}

// resolveGroupID is step 0: get-or-create the registry row for the physical
// (platform, group, thread) triple and return its surrogate id.
func resolveGroupID(ctx context.Context, q *sqlc.Queries, platform, platformGroupID, platformThreadID string) (string, error) {
	state, err := q.GetGroupStateByTriple(ctx, sqlc.GetGroupStateByTripleParams{
		Platform:         platform,
		PlatformGroupID:  platformGroupID,
		PlatformThreadID: platformThreadID,
	})
	if err == nil {
		return state.ID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("eventlog: get group state: %w", err)
	}
	created, err := q.CreateGroupState(ctx, sqlc.CreateGroupStateParams{
		ID:               uuid.NewString(),
		Platform:         platform,
		PlatformGroupID:  platformGroupID,
		PlatformThreadID: platformThreadID,
	})
	if err != nil {
		return "", fmt.Errorf("eventlog: create group state: %w", err)
	}
	return created.ID, nil
}

// lookup performs the in-lock dedup check. Tier 1: stable platform_message_id.
// Tier 2: fallback idempotency key. No key → never a duplicate, always insert.
func lookup(ctx context.Context, q *sqlc.Queries, groupID string, msg Message, idemKey sql.NullString) (sqlc.CtxGroupMessage, bool, error) {
	if msg.PlatformMessageID != "" {
		row, err := q.GetGroupMessageByPlatformID(ctx, sqlc.GetGroupMessageByPlatformIDParams{
			GroupID:           groupID,
			PlatformMessageID: nullString(msg.PlatformMessageID),
		})
		return found(row, err)
	}
	if idemKey.Valid {
		row, err := q.GetGroupMessageByIdempotencyKey(ctx, idemKey)
		return found(row, err)
	}
	return sqlc.CtxGroupMessage{}, false, nil
}

func found(row sqlc.CtxGroupMessage, err error) (sqlc.CtxGroupMessage, bool, error) {
	if err == nil {
		return row, true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return sqlc.CtxGroupMessage{}, false, nil
	}
	return sqlc.CtxGroupMessage{}, false, fmt.Errorf("eventlog: dedup lookup: %w", err)
}

// idempotencyKey derives the tier-2 fallback key. It is set only when there is
// no stable platform_message_id but a non-zero platform timestamp exists, so a
// redelivery without a platform id still collapses to one row.
func idempotencyKey(groupID string, msg Message) sql.NullString {
	if msg.PlatformMessageID != "" || msg.PlatformTimestamp.IsZero() {
		return sql.NullString{}
	}
	h := sha256.New()
	for _, part := range []string{groupID, msg.ActorID, platformTimestamp(msg.PlatformTimestamp), msg.Content} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return sql.NullString{String: hex.EncodeToString(h.Sum(nil)), Valid: true}
}

// platformTimestamp renders a timestamp as RFC3339Nano UTC, or "" for the zero
// value. The same string feeds both the stored column and the idempotency hash.
func platformTimestamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func nullString(s string) sql.NullString {
	if strings.TrimSpace(s) == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
