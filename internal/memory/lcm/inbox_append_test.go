package lcm_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/lcm"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func newInboxTestSessions(t *testing.T, p memory.Provider) (memory.Session, memory.Session) {
	t.Helper()
	now := time.Now().UTC()
	source := newLCMTestSession("inbox-source-" + uuid.NewString())
	target := newLCMTestSession("inbox-target-" + uuid.NewString())
	manager := p.(memory.SessionManager)
	for _, item := range []struct {
		session memory.Session
		kind    string
	}{
		{session: source, kind: "main"},
		{session: target, kind: "chat"},
	} {
		ctx := authz.WithAgentID(authz.WithUserID(t.Context(), item.session.UserID), item.session.AgentID)
		if err := manager.SaveInfo(ctx, memory.SessionInfo{
			ID: item.session.ID, AgentID: item.session.AgentID, UserID: item.session.UserID,
			Channel: item.session.Channel, Kind: item.kind, CreatedAt: now, LastActive: now,
		}); err != nil {
			t.Fatalf("SaveInfo(%s): %v", item.session.ID, err)
		}
	}
	return source, target
}

func enqueueInboxTestMessage(t *testing.T, q *sqlc.Queries, source, target memory.Session, content string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := q.EnqueueSessionInbox(t.Context(), sqlc.EnqueueSessionInboxParams{
		ID: id, SourceSessionID: source.ID, TargetSessionID: target.ID,
		ActorID: source.AgentID, Content: content,
	}); err != nil {
		t.Fatalf("EnqueueSessionInbox: %v", err)
	}
	return id
}

func inboxActorContext(ctx context.Context, source, target memory.Session) context.Context {
	ctx = authz.WithUserID(ctx, target.UserID)
	ctx = authz.WithAgentID(ctx, target.AgentID)
	return eventlog.WithMessageActor(ctx, eventlog.MessageActor{
		Type: eventlog.ActorAgent, ID: source.AgentID, SourceSessionID: source.ID,
	})
}

func TestAppendInboxInputClaimsAndAppendsExactlyOnce(t *testing.T) {
	db := newLCMTestDB(t)
	defer db.Close()
	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	source, target := newInboxTestSessions(t, p)
	q := sqlc.New(db)
	const content = "durable hello"
	inboxID := enqueueInboxTestMessage(t, q, source, target, content)
	ctx := inboxActorContext(t.Context(), source, target)

	if err := p.AppendInboxInput(ctx, target, inboxID, ai.UserMessage{Content: content}); err != nil {
		t.Fatalf("AppendInboxInput: %v", err)
	}
	if err := p.AppendInboxInput(ctx, target, inboxID, ai.UserMessage{Content: content}); !errors.Is(err, memory.ErrInboxNotPending) {
		t.Fatalf("second AppendInboxInput error = %v, want ErrInboxNotPending", err)
	}

	inbox, err := q.GetSessionInbox(t.Context(), inboxID)
	if err != nil {
		t.Fatalf("GetSessionInbox: %v", err)
	}
	if !inbox.DeliveredAt.Valid || inbox.FailedAt.Valid {
		t.Fatalf("inbox terminal state = delivered:%v failed:%v", inbox.DeliveredAt.Valid, inbox.FailedAt.Valid)
	}
	conversation, err := q.GetConversationBySessionID(t.Context(), sqlc.GetConversationBySessionIDParams{
		SessionID: target.ID,
		UserID:    pgtype.Text{String: target.UserID, Valid: true},
		AgentID:   pgtype.Text{String: target.AgentID, Valid: true},
	})
	if err != nil {
		t.Fatalf("GetConversationBySessionID: %v", err)
	}
	messages, err := q.GetMessagesByConversation(t.Context(), conversation.ID)
	if err != nil {
		t.Fatalf("GetMessagesByConversation: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("message count = %d, want 1", len(messages))
	}
	message := messages[0]
	if !message.InboxID.Valid || message.InboxID.String != inboxID {
		t.Fatalf("message inbox_id = %+v, want %s", message.InboxID, inboxID)
	}
	if message.ActorType != string(eventlog.ActorAgent) || message.ActorID.String != source.AgentID || message.SourceSessionID.String != source.ID {
		t.Fatalf("message provenance = type:%q actor:%+v source:%+v", message.ActorType, message.ActorID, message.SourceSessionID)
	}
}

func TestAppendInboxInputFactMismatchLeavesInboxPending(t *testing.T) {
	db := newLCMTestDB(t)
	defer db.Close()
	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	source, target := newInboxTestSessions(t, p)
	q := sqlc.New(db)
	inboxID := enqueueInboxTestMessage(t, q, source, target, "authoritative content")

	err = p.AppendInboxInput(inboxActorContext(t.Context(), source, target), target, inboxID, ai.UserMessage{Content: "different content"})
	if !errors.Is(err, memory.ErrInboxNotPending) {
		t.Fatalf("AppendInboxInput error = %v, want ErrInboxNotPending", err)
	}
	inbox, err := q.GetSessionInbox(t.Context(), inboxID)
	if err != nil {
		t.Fatalf("GetSessionInbox: %v", err)
	}
	if inbox.DeliveredAt.Valid || inbox.FailedAt.Valid {
		t.Fatalf("mismatched claim changed state: delivered:%v failed:%v", inbox.DeliveredAt.Valid, inbox.FailedAt.Valid)
	}
	var count int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM ctx_message WHERE inbox_id = $1`, inboxID).Scan(&count); err != nil {
		t.Fatalf("count inbox messages: %v", err)
	}
	if count != 0 {
		t.Fatalf("inbox message count = %d, want 0", count)
	}
}

func TestAppendInboxInputRollsBackClaimWhenTranscriptInsertFails(t *testing.T) {
	db := newLCMTestDB(t)
	defer db.Close()
	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	source, target := newInboxTestSessions(t, p)
	q := sqlc.New(db)
	const content = "must roll back"
	inboxID := enqueueInboxTestMessage(t, q, source, target, content)
	if _, err := db.Exec(t.Context(), `
		CREATE FUNCTION fail_inbox_message_insert() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.inbox_id IS NOT NULL THEN RAISE EXCEPTION 'injected inbox insert failure'; END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER fail_inbox_message_insert BEFORE INSERT ON ctx_message
		FOR EACH ROW EXECUTE FUNCTION fail_inbox_message_insert();
	`); err != nil {
		t.Fatalf("install failure trigger: %v", err)
	}

	err = p.AppendInboxInput(inboxActorContext(t.Context(), source, target), target, inboxID, ai.UserMessage{Content: content})
	if err == nil {
		t.Fatal("AppendInboxInput succeeded, want injected failure")
	}
	inbox, getErr := q.GetSessionInbox(t.Context(), inboxID)
	if getErr != nil {
		t.Fatalf("GetSessionInbox: %v", getErr)
	}
	if inbox.DeliveredAt.Valid || inbox.FailedAt.Valid {
		t.Fatalf("claim was not rolled back: delivered:%v failed:%v", inbox.DeliveredAt.Valid, inbox.FailedAt.Valid)
	}
	var count int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM ctx_message WHERE inbox_id = $1`, inboxID).Scan(&count); err != nil {
		t.Fatalf("count inbox messages: %v", err)
	}
	if count != 0 {
		t.Fatalf("inbox message count = %d, want 0", count)
	}
}

func TestAppendInboxInputConcurrentClaimsCreateOneMessage(t *testing.T) {
	db := newLCMTestDB(t)
	defer db.Close()
	first, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("new first provider: %v", err)
	}
	second, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("new second provider: %v", err)
	}
	source, target := newInboxTestSessions(t, first)
	q := sqlc.New(db)
	const content = "only once"
	inboxID := enqueueInboxTestMessage(t, q, source, target, content)

	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, provider := range []*lcm.Provider{first, second} {
		wg.Add(1)
		go func(provider *lcm.Provider) {
			defer wg.Done()
			<-start
			ctx := inboxActorContext(context.Background(), source, target)
			results <- provider.AppendInboxInput(ctx, target, inboxID, ai.UserMessage{Content: content})
		}(provider)
	}
	close(start)
	wg.Wait()
	close(results)

	var succeeded, lost int
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, memory.ErrInboxNotPending):
			lost++
		default:
			t.Fatalf("unexpected claim error: %v", err)
		}
	}
	if succeeded != 1 || lost != 1 {
		t.Fatalf("claim results = succeeded:%d lost:%d, want 1/1", succeeded, lost)
	}
	var count int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM ctx_message WHERE inbox_id = $1`, inboxID).Scan(&count); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("count inbox messages: %v", err)
	}
	if count != 1 {
		t.Fatalf("inbox message count = %d, want 1", count)
	}
}
