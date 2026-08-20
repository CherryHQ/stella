package groupingest_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/groupingest"
	"github.com/CherryHQ/stella/internal/memory/memorywrite"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func openTestDB(t *testing.T) (*pgxpool.Pool, *sqlc.Queries) {
	t.Helper()
	db := dbtest.New(t)
	return db, sqlc.New(db)
}

func resolveGroup(t *testing.T, store *eventlog.Store, platform, groupID, threadID string) string {
	t.Helper()
	id, err := store.ResolveGroupID(context.Background(), platform, groupID, threadID)
	if err != nil {
		t.Fatalf("resolve group: %v", err)
	}
	return id
}

func appendHuman(t *testing.T, store *eventlog.Store, groupID, actorID, content string) eventlog.AppendResult {
	t.Helper()
	r, err := store.AppendGroupMessage(context.Background(), eventlog.Message{
		Platform:        "test",
		PlatformGroupID: groupID,
		ActorType:       eventlog.ActorHuman,
		ActorID:         actorID,
		Content:         content,
	})
	if err != nil {
		t.Fatalf("append human: %v", err)
	}
	return r
}

func appendAgent(t *testing.T, store *eventlog.Store, groupID, actorID, content string) eventlog.AppendResult {
	t.Helper()
	r, err := store.AppendGroupMessage(context.Background(), eventlog.Message{
		Platform:        "test",
		PlatformGroupID: groupID,
		ActorType:       eventlog.ActorAgent,
		ActorID:         actorID,
		Content:         content,
	})
	if err != nil {
		t.Fatalf("append agent: %v", err)
	}
	return r
}

func appendSystem(t *testing.T, store *eventlog.Store, groupID, actorID, content string) eventlog.AppendResult {
	t.Helper()
	r, err := store.AppendGroupMessage(context.Background(), eventlog.Message{
		Platform:        "test",
		PlatformGroupID: groupID,
		ActorType:       eventlog.ActorSystem,
		ActorID:         actorID,
		Content:         content,
	})
	if err != nil {
		t.Fatalf("append system: %v", err)
	}
	return r
}

// fakeExtractor returns a fixed result. Set Err to simulate transient failures.
type fakeExtractor struct {
	Calls int
	Err   error
	Fn    func(req groupingest.ExtractRequest) (groupingest.ExtractResult, error)
}

func (f *fakeExtractor) Extract(_ context.Context, req groupingest.ExtractRequest) (groupingest.ExtractResult, error) {
	f.Calls++
	if f.Err != nil {
		return groupingest.ExtractResult{}, f.Err
	}
	if f.Fn != nil {
		return f.Fn(req)
	}
	var facts []string
	actors := make(map[string][]string)
	for _, m := range req.Messages {
		facts = append(facts, m.Content)
		actors[m.ActorID] = append(actors[m.ActorID], "fact about "+m.ActorID)
	}
	mem := req.CurrentGroupMemory
	for _, f := range facts {
		if mem != "" {
			mem += "\n"
		}
		mem += f
	}
	return groupingest.ExtractResult{
		GroupMemory: mem,
		UserFacts:   actors,
	}, nil
}

func TestBasicIngestFlow(t *testing.T) {
	db, q := openTestDB(t)
	ctx := context.Background()
	store := eventlog.NewStore(db)

	appendHuman(t, store, "g1", "alice", "I like Go")
	appendHuman(t, store, "g1", "bob", "I prefer Rust")

	var userFacts []string
	ext := &fakeExtractor{}
	ing := groupingest.New(groupingest.Config{
		DB:        db,
		Q:         q,
		Extractor: ext,
		UserWriter: func(_ context.Context, actorID, fact string) error {
			userFacts = append(userFacts, actorID+":"+fact)
			return nil
		},
	})

	if err := ing.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if ext.Calls != 1 {
		t.Fatalf("expected 1 extraction call, got %d", ext.Calls)
	}

	got, err := memorywrite.GetGroupMemory(ctx, q, resolveGroup(t, store, "test", "g1", ""))
	if err != nil {
		t.Fatalf("get group memory: %v", err)
	}
	if got == "" {
		t.Fatal("expected group memory to be written")
	}

	if len(userFacts) == 0 {
		t.Fatal("expected user facts to be written")
	}
}

func TestCursorPersistence(t *testing.T) {
	db, q := openTestDB(t)
	ctx := context.Background()
	store := eventlog.NewStore(db)

	appendHuman(t, store, "g1", "alice", "first message")

	ext := &fakeExtractor{}
	ing := groupingest.New(groupingest.Config{
		DB:        db,
		Q:         q,
		Extractor: ext,
	})

	if err := ing.RunOnce(ctx); err != nil {
		t.Fatalf("first RunOnce: %v", err)
	}
	if ext.Calls != 1 {
		t.Fatalf("expected 1 call, got %d", ext.Calls)
	}

	// Second run with no new messages should not call extractor.
	if err := ing.RunOnce(ctx); err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	if ext.Calls != 1 {
		t.Fatalf("expected still 1 call after no-op run, got %d", ext.Calls)
	}

	// Add a new message; third run should process only it.
	appendHuman(t, store, "g1", "bob", "second message")
	if err := ing.RunOnce(ctx); err != nil {
		t.Fatalf("third RunOnce: %v", err)
	}
	if ext.Calls != 2 {
		t.Fatalf("expected 2 calls after new message, got %d", ext.Calls)
	}
}

func TestTransientFailureDoesNotAdvanceCursor(t *testing.T) {
	db, q := openTestDB(t)
	ctx := context.Background()
	store := eventlog.NewStore(db)

	appendHuman(t, store, "g1", "alice", "important data")

	ext := &fakeExtractor{Err: errors.New("LLM timeout")}
	ing := groupingest.New(groupingest.Config{
		DB:        db,
		Q:         q,
		Extractor: ext,
	})

	err := ing.RunOnce(ctx)
	if err == nil {
		t.Fatal("expected error from failed extraction")
	}

	// Cursor should NOT have advanced.
	groupID := resolveGroup(t, store, "test", "g1", "")
	cursor, cerr := q.GetIngestCursor(ctx, sqlc.GetIngestCursorParams{
		GroupID:  groupID,
		Pipeline: groupingest.PipelineMemoryIngest,
	})
	if cerr == nil && cursor.LastSeq > 0 {
		t.Fatalf("cursor should not advance on failure, got last_seq=%d", cursor.LastSeq)
	}

	// Fix the extractor; next run should succeed.
	ext.Err = nil
	if err := ing.RunOnce(ctx); err != nil {
		t.Fatalf("retry RunOnce: %v", err)
	}
	if ext.Calls != 2 {
		t.Fatalf("expected 2 calls (1 fail + 1 success), got %d", ext.Calls)
	}
}

func TestDeadLetterBadMessage(t *testing.T) {
	db, q := openTestDB(t)
	ctx := context.Background()
	store := eventlog.NewStore(db)

	// Insert a message with empty content directly via raw SQL to simulate a bad message.
	groupID := resolveGroup(t, store, "test", "g1", "")
	// Normal message first.
	appendHuman(t, store, "g1", "alice", "good message")
	// Insert bad message with empty content by updating the row.
	r2, err := store.AppendGroupMessage(ctx, eventlog.Message{
		Platform:        "test",
		PlatformGroupID: "g1",
		ActorType:       eventlog.ActorHuman,
		ActorID:         "bob",
		Content:         "placeholder",
	})
	if err != nil {
		t.Fatalf("append placeholder: %v", err)
	}
	// Overwrite content to empty to simulate a bad message.
	if _, err := db.Exec(ctx, `UPDATE ctx_group_message SET content = '' WHERE id = $1`, r2.Message.ID); err != nil {
		t.Fatalf("overwrite content: %v", err)
	}

	// Add a third good message after the bad one.
	appendHuman(t, store, "g1", "alice", "third good")

	ext := &fakeExtractor{}
	ing := groupingest.New(groupingest.Config{
		DB:        db,
		Q:         q,
		Extractor: ext,
	})

	if err := ing.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	// Extractor should have been called with 2 messages (the 2 good ones).
	if ext.Calls != 1 {
		t.Fatalf("expected 1 extraction call, got %d", ext.Calls)
	}

	// Bad message should be in dead-letter table.
	isErr, err := q.IsIngestError(ctx, sqlc.IsIngestErrorParams{
		GroupID:  groupID,
		Pipeline: groupingest.PipelineMemoryIngest,
		Seq:      r2.Seq,
	})
	if err != nil {
		t.Fatalf("check dead-letter: %v", err)
	}
	if !isErr {
		t.Fatal("expected bad message to be dead-lettered")
	}

	// Cursor should have advanced past all three messages (including the bad one).
	cursor, err := q.GetIngestCursor(ctx, sqlc.GetIngestCursorParams{
		GroupID:  groupID,
		Pipeline: groupingest.PipelineMemoryIngest,
	})
	if err != nil {
		t.Fatalf("get cursor: %v", err)
	}
	if cursor.LastSeq < 3 {
		t.Fatalf("cursor should advance past dead-lettered message, got last_seq=%d", cursor.LastSeq)
	}
}

func TestAgentMessagesExcluded(t *testing.T) {
	db, q := openTestDB(t)
	ctx := context.Background()
	store := eventlog.NewStore(db)

	appendHuman(t, store, "g1", "alice", "human says hello")
	appendAgent(t, store, "g1", "agent-1", "agent responds")

	ext := &fakeExtractor{
		Fn: func(req groupingest.ExtractRequest) (groupingest.ExtractResult, error) {
			for _, m := range req.Messages {
				if m.ActorID == "agent-1" {
					return groupingest.ExtractResult{}, errors.New("agent message should not be in extraction")
				}
			}
			return groupingest.ExtractResult{GroupMemory: "extracted"}, nil
		},
	}

	ing := groupingest.New(groupingest.Config{
		DB:        db,
		Q:         q,
		Extractor: ext,
	})

	if err := ing.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	// Verify group memory was written (extraction succeeded with only human message).
	groupID := resolveGroup(t, store, "test", "g1", "")
	got, err := memorywrite.GetGroupMemory(ctx, q, groupID)
	if err != nil {
		t.Fatalf("get group memory: %v", err)
	}
	if got != "extracted" {
		t.Fatalf("expected 'extracted', got %q", got)
	}

	// Cursor should advance past both messages (human + agent).
	cursor, err := q.GetIngestCursor(ctx, sqlc.GetIngestCursorParams{
		GroupID:  groupID,
		Pipeline: groupingest.PipelineMemoryIngest,
	})
	if err != nil {
		t.Fatalf("get cursor: %v", err)
	}
	if cursor.LastSeq < 2 {
		t.Fatalf("cursor should advance past agent message too, got last_seq=%d", cursor.LastSeq)
	}
}

func TestSystemActorAcceptedByValidatorsAndSkippedByIngest(t *testing.T) {
	db, q := openTestDB(t)
	ctx := context.Background()
	store := eventlog.NewStore(db)
	result := appendSystem(t, store, "g1", "nudge", "agent-1, please continue")

	ext := &fakeExtractor{}
	ing := groupingest.New(groupingest.Config{DB: db, Q: q, Extractor: ext})
	if err := ing.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if ext.Calls != 0 {
		t.Fatalf("system row reached human-memory extractor %d times", ext.Calls)
	}
	groupID := resolveGroup(t, store, "test", "g1", "")
	cursor, err := q.GetIngestCursor(ctx, sqlc.GetIngestCursorParams{GroupID: groupID, Pipeline: groupingest.PipelineMemoryIngest})
	if err != nil {
		t.Fatal(err)
	}
	if cursor.LastSeq != result.Seq {
		t.Fatalf("system row cursor = %d, want %d", cursor.LastSeq, result.Seq)
	}
}

func TestOnlyAgentMessages(t *testing.T) {
	db, q := openTestDB(t)
	ctx := context.Background()
	store := eventlog.NewStore(db)

	appendAgent(t, store, "g1", "agent-1", "agent message 1")
	appendAgent(t, store, "g1", "agent-2", "agent message 2")

	ext := &fakeExtractor{}
	ing := groupingest.New(groupingest.Config{
		DB:        db,
		Q:         q,
		Extractor: ext,
	})

	if err := ing.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	// Extractor should not have been called (no human messages).
	if ext.Calls != 0 {
		t.Fatalf("expected 0 extraction calls for agent-only messages, got %d", ext.Calls)
	}

	// Cursor should still advance past agent messages.
	groupID := resolveGroup(t, store, "test", "g1", "")
	cursor, err := q.GetIngestCursor(ctx, sqlc.GetIngestCursorParams{
		GroupID:  groupID,
		Pipeline: groupingest.PipelineMemoryIngest,
	})
	if err != nil {
		t.Fatalf("get cursor: %v", err)
	}
	if cursor.LastSeq < 2 {
		t.Fatalf("cursor should advance past agent messages, got last_seq=%d", cursor.LastSeq)
	}
}

func TestConcurrentProcessGroupNoDoubleWrite(t *testing.T) {
	db, q := openTestDB(t)
	ctx := context.Background()
	store := eventlog.NewStore(db)

	for range 10 {
		appendHuman(t, store, "g1", "alice", "msg")
	}

	var extractCalls atomic.Int32
	ext := &fakeExtractor{
		Fn: func(req groupingest.ExtractRequest) (groupingest.ExtractResult, error) {
			extractCalls.Add(1)
			return groupingest.ExtractResult{GroupMemory: "extracted"}, nil
		},
	}

	ing := groupingest.New(groupingest.Config{
		DB:        db,
		Q:         q,
		Extractor: ext,
	})

	var wg sync.WaitGroup
	groupID := resolveGroup(t, store, "test", "g1", "")

	for range 5 {
		wg.Go(func() {
			_ = ing.ProcessGroup(ctx, groupID)
		})
	}
	wg.Wait()

	// Only one goroutine should have won the lock and extracted.
	calls := extractCalls.Load()
	if calls != 1 {
		t.Fatalf("expected exactly 1 extraction call from concurrent attempts, got %d", calls)
	}
}

func TestMultipleGroups(t *testing.T) {
	db, q := openTestDB(t)
	ctx := context.Background()
	store := eventlog.NewStore(db)

	appendHuman(t, store, "g1", "alice", "group 1 message")
	appendHuman(t, store, "g2", "bob", "group 2 message")

	ext := &fakeExtractor{}
	ing := groupingest.New(groupingest.Config{
		DB:        db,
		Q:         q,
		Extractor: ext,
	})

	if err := ing.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if ext.Calls != 2 {
		t.Fatalf("expected 2 extraction calls (one per group), got %d", ext.Calls)
	}

	g1 := resolveGroup(t, store, "test", "g1", "")
	g2 := resolveGroup(t, store, "test", "g2", "")

	mem1, _ := memorywrite.GetGroupMemory(ctx, q, g1)
	mem2, _ := memorywrite.GetGroupMemory(ctx, q, g2)
	if mem1 == "" {
		t.Fatal("expected group 1 memory")
	}
	if mem2 == "" {
		t.Fatal("expected group 2 memory")
	}
}
