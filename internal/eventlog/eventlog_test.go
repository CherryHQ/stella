package eventlog_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func newStore(t *testing.T) *eventlog.Store {
	t.Helper()
	db := dbtest.New(t)
	return eventlog.NewStore(db)
}

func humanMsg() eventlog.Message {
	return eventlog.Message{
		Platform:        "telegram",
		PlatformGroupID: "g1",
		ActorType:       eventlog.ActorHuman,
		ActorID:         "u1",
		Content:         `[{"text":"hi"}]`,
	}
}

func TestAppendInsertsFirstMessageWithSeqOne(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	msg := humanMsg()
	msg.PlatformMessageID = "m1"
	msg.ActorDisplayName = "Alice"

	res, err := s.AppendGroupMessage(ctx, msg)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if !res.Inserted {
		t.Fatal("first message should be inserted")
	}
	if res.Seq != 1 {
		t.Fatalf("first seq = %d, want 1", res.Seq)
	}
	if res.GroupID == "" {
		t.Fatal("group id should be resolved")
	}
	if !res.Message.ActorDisplayName.Valid || res.Message.ActorDisplayName.String != "Alice" {
		t.Fatalf("actor display name = %#v, want Alice", res.Message.ActorDisplayName)
	}
}

func TestAppendPersistsContentBlocks(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	msg := humanMsg()
	msg.PlatformMessageID = "m-blocks"
	msg.ContentBlocks = []byte(`[{"kind":"text","text":"hi"},{"kind":"image","data":"aGk=","mime_type":"image/png"}]`)

	res, err := s.AppendGroupMessage(ctx, msg)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	// jsonb normalizes formatting; compare decoded blocks, not raw bytes.
	blocks, err := ai.UnmarshalContentBlocks(res.Message.ContentBlocks)
	if err != nil {
		t.Fatalf("unmarshal stored blocks: %v", err)
	}
	if len(blocks) != 2 || !ai.HasImage(blocks) {
		t.Fatalf("stored blocks = %#v, want text+image", blocks)
	}
}

func TestAppendWithoutContentBlocksStoresEmptyArray(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	msg := humanMsg()
	msg.PlatformMessageID = "m-noblocks"

	res, err := s.AppendGroupMessage(ctx, msg)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if got := string(res.Message.ContentBlocks); got != "[]" {
		t.Fatalf("content_blocks = %s, want []", got)
	}
}

func TestAppendOnInsertedRunsOnlyForNewRows(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	msg := humanMsg()
	msg.PlatformMessageID = "m1"

	called := 0
	first, err := s.AppendGroupMessage(ctx, msg, eventlog.WithOnInserted(func(ctx context.Context, q *sqlc.Queries, result eventlog.AppendResult) error {
		called++
		if result.GroupID == "" || result.Message.ID == "" {
			t.Fatal("callback should receive populated append result")
		}
		if _, err := q.GetGroupStateByID(ctx, result.GroupID); err != nil {
			t.Fatalf("callback should run on transaction-bound queries: %v", err)
		}
		return nil
	}))
	if err != nil {
		t.Fatalf("first append: %v", err)
	}
	if !first.Inserted || called != 1 {
		t.Fatalf("first append inserted=%v callback calls=%d, want inserted and one callback", first.Inserted, called)
	}

	second, err := s.AppendGroupMessage(ctx, msg, eventlog.WithOnInserted(func(context.Context, *sqlc.Queries, eventlog.AppendResult) error {
		called++
		return nil
	}))
	if err != nil {
		t.Fatalf("second append: %v", err)
	}
	if second.Inserted {
		t.Fatal("duplicate should not insert")
	}
	if called != 1 {
		t.Fatalf("callback calls after duplicate = %d, want 1", called)
	}
}

func TestAppendOnInsertedErrorRollsBack(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	boom := errors.New("boom")
	msg := humanMsg()
	msg.PlatformMessageID = "m1"

	if _, err := s.AppendGroupMessage(ctx, msg, eventlog.WithOnInserted(func(context.Context, *sqlc.Queries, eventlog.AppendResult) error {
		return boom
	})); !errors.Is(err, boom) {
		t.Fatalf("append error = %v, want boom", err)
	}

	res, err := s.AppendGroupMessage(ctx, msg)
	if err != nil {
		t.Fatalf("append after rollback: %v", err)
	}
	if !res.Inserted || res.Seq != 1 {
		t.Fatalf("append after rollback inserted=%v seq=%d, want inserted seq 1", res.Inserted, res.Seq)
	}
}

func TestDedupByPlatformMessageID(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	msg := humanMsg()
	msg.PlatformMessageID = "m1"

	first, err := s.AppendGroupMessage(ctx, msg)
	if err != nil {
		t.Fatalf("first append: %v", err)
	}

	// Same platform message id delivered again (e.g. by another bot).
	msg.SourceChannelID = "another-bot"
	second, err := s.AppendGroupMessage(ctx, msg)
	if err != nil {
		t.Fatalf("second append: %v", err)
	}
	if second.Inserted {
		t.Fatal("redelivery must not insert a new row")
	}
	if second.Seq != first.Seq {
		t.Fatalf("redelivery seq = %d, want %d (no seq consumed)", second.Seq, first.Seq)
	}
}

func TestDedupByIdempotencyKey(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	// No stable platform message id, but a high-precision timestamp → tier-2 key.
	ts := time.Date(2026, 6, 4, 8, 2, 27, 123456789, time.UTC)
	msg := humanMsg()
	msg.PlatformTimestamp = ts

	first, err := s.AppendGroupMessage(ctx, msg)
	if err != nil {
		t.Fatalf("first append: %v", err)
	}
	if !first.Inserted {
		t.Fatal("first should insert")
	}

	second, err := s.AppendGroupMessage(ctx, msg)
	if err != nil {
		t.Fatalf("second append: %v", err)
	}
	if second.Inserted {
		t.Fatal("identical timestamped redelivery must dedup")
	}
	if second.Seq != first.Seq {
		t.Fatalf("redelivery seq = %d, want %d", second.Seq, first.Seq)
	}
}

func TestNoDedupKeyAlwaysInserts(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	// Neither platform message id nor timestamp → "rather duplicate than drop".
	msg := humanMsg()

	first, err := s.AppendGroupMessage(ctx, msg)
	if err != nil {
		t.Fatalf("first append: %v", err)
	}
	second, err := s.AppendGroupMessage(ctx, msg)
	if err != nil {
		t.Fatalf("second append: %v", err)
	}
	if !first.Inserted || !second.Inserted {
		t.Fatal("messages with no dedup key must both insert")
	}
	if second.Seq == first.Seq {
		t.Fatalf("distinct inserts must get distinct seqs, both = %d", first.Seq)
	}
}

func TestThreadIsSeparateGroup(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	base := humanMsg()
	base.PlatformMessageID = "m1"

	threaded := humanMsg()
	threaded.PlatformThreadID = "topic-7"
	threaded.PlatformMessageID = "m1" // same id, different thread

	a, err := s.AppendGroupMessage(ctx, base)
	if err != nil {
		t.Fatalf("base append: %v", err)
	}
	b, err := s.AppendGroupMessage(ctx, threaded)
	if err != nil {
		t.Fatalf("threaded append: %v", err)
	}
	if a.GroupID == b.GroupID {
		t.Fatal("a thread must resolve to its own group registry row")
	}
	if !b.Inserted {
		t.Fatal("same platform id in a different thread is a distinct message")
	}
}

func TestConcurrentAppendsHaveContiguousUniqueSeqs(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	const n = 25
	var wg sync.WaitGroup
	seqs := make([]int64, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			msg := humanMsg()
			msg.PlatformMessageID = string(rune('a'+i%26)) + time.Now().Format("150405.000000000")
			res, err := s.AppendGroupMessage(ctx, msg)
			errs[i] = err
			seqs[i] = res.Seq
		}(i)
	}
	wg.Wait()

	seen := make(map[int64]bool, n)
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("append %d: %v", i, errs[i])
		}
		if seen[seqs[i]] {
			t.Fatalf("duplicate seq %d", seqs[i])
		}
		seen[seqs[i]] = true
	}
	for want := int64(1); want <= n; want++ {
		if !seen[want] {
			t.Fatalf("seq %d missing — not contiguous 1..%d", want, n)
		}
	}
}

func TestResolveGroupIDIdempotent(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	id1, err := s.ResolveGroupID(ctx, "telegram", "g1", "")
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if id1 == "" {
		t.Fatal("expected non-empty group_id")
	}

	id2, err := s.ResolveGroupID(ctx, "telegram", "g1", "")
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if id2 != id1 {
		t.Fatalf("resolve not idempotent: %q vs %q", id1, id2)
	}
}

func TestResolveGroupIDThreadsAreDistinct(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	idNoThread, err := s.ResolveGroupID(ctx, "telegram", "g1", "")
	if err != nil {
		t.Fatalf("resolve no thread: %v", err)
	}
	idThread1, err := s.ResolveGroupID(ctx, "telegram", "g1", "t1")
	if err != nil {
		t.Fatalf("resolve thread1: %v", err)
	}
	if idThread1 == idNoThread {
		t.Fatal("thread should produce a different group_id than no-thread")
	}
}

func TestResolveGroupIDConsistentWithAppend(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	resolvedID, err := s.ResolveGroupID(ctx, "telegram", "g1", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	msg := humanMsg()
	msg.PlatformMessageID = "m1"
	res, err := s.AppendGroupMessage(ctx, msg)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if res.GroupID != resolvedID {
		t.Fatalf("append returned group_id %q, resolve returned %q", res.GroupID, resolvedID)
	}
}

func TestAppendToGroup(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	// First create a group via normal append.
	msg := humanMsg()
	msg.PlatformMessageID = "m1"
	res, err := s.AppendGroupMessage(ctx, msg)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Append agent response via AppendToGroup.
	agentRes, err := s.AppendToGroup(ctx, res.GroupID, eventlog.GroupMessage{
		ActorType:        eventlog.ActorAgent,
		ActorID:          "agent-1",
		ActorDisplayName: "Support",
		Content:          "Hello from agent!",
	})
	if err != nil {
		t.Fatalf("append to group: %v", err)
	}
	if !agentRes.Inserted {
		t.Fatal("agent response should be inserted")
	}
	if agentRes.Seq != 2 {
		t.Fatalf("agent response seq = %d, want 2", agentRes.Seq)
	}
	if agentRes.Message.ActorType != "agent" {
		t.Fatalf("actor_type = %q, want agent", agentRes.Message.ActorType)
	}
	if agentRes.Message.Content != "Hello from agent!" {
		t.Fatalf("content = %q, want %q", agentRes.Message.Content, "Hello from agent!")
	}
	if !agentRes.Message.ActorDisplayName.Valid || agentRes.Message.ActorDisplayName.String != "Support" {
		t.Fatalf("actor display name = %#v, want Support", agentRes.Message.ActorDisplayName)
	}
}

func TestAppendToGroupValidation(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	// Empty group ID.
	if _, err := s.AppendToGroup(ctx, "", eventlog.GroupMessage{
		ActorType: eventlog.ActorAgent, ActorID: "a1", Content: "hi",
	}); err == nil {
		t.Fatal("expected error for empty group_id")
	}

	// Empty actor ID.
	if _, err := s.AppendToGroup(ctx, "g1", eventlog.GroupMessage{
		ActorType: eventlog.ActorAgent, ActorID: "", Content: "hi",
	}); err == nil {
		t.Fatal("expected error for empty actor_id")
	}

	// Bad actor type.
	if _, err := s.AppendToGroup(ctx, "g1", eventlog.GroupMessage{
		ActorType: "bot", ActorID: "a1", Content: "hi",
	}); err == nil {
		t.Fatal("expected error for invalid actor_type")
	}
}

func TestValidation(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	cases := map[string]func(*eventlog.Message){
		"missing platform": func(m *eventlog.Message) { m.Platform = "" },
		"missing group id": func(m *eventlog.Message) { m.PlatformGroupID = "" },
		"missing actor id": func(m *eventlog.Message) { m.ActorID = "" },
		"bad actor type":   func(m *eventlog.Message) { m.ActorType = "bot" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			msg := humanMsg()
			mutate(&msg)
			if _, err := s.AppendGroupMessage(ctx, msg); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
