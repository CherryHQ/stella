package channel

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/auth"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func newChannelFIFOQueries(t *testing.T) *sqlc.Queries {
	t.Helper()
	_, q := newChannelFIFODB(t)
	return q
}

func newChannelFIFODB(t *testing.T) (*pgxpool.Pool, *sqlc.Queries) {
	t.Helper()
	db := dbtest.New(t)
	q := sqlc.New(db)
	ctx := t.Context()
	if _, err := q.CreateAgent(ctx, sqlc.CreateAgentParams{
		ID: "fifo-agent", Name: "FIFO Agent", Workspace: t.TempDir(),
		Sandbox: json.RawMessage(`{}`), Scope: "system", Enabled: true,
	}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if err := q.CreateWebChannelIfNotExists(ctx, sqlc.CreateWebChannelIfNotExistsParams{
		ID: "fifo-channel", AgentID: pgtype.Text{String: "fifo-agent", Valid: true},
	}); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	return db, q
}

func createFIFO(t *testing.T, q *sqlc.Queries, id, binding, source, kind string, payload, media json.RawMessage, attachmentBytes int64, expected any) (sqlc.ChannelBindingFifo, error) {
	t.Helper()
	return q.CreateChannelBindingFIFO(t.Context(), sqlc.CreateChannelBindingFIFOParams{
		ID: id, ChannelID: "fifo-channel", BindingKey: binding, PrincipalID: "principal-1",
		SourceKey: source, Kind: kind, Payload: payload, ImmutableMedia: media,
		AttachmentBytes: attachmentBytes, ExpectedSessionID: expected,
	})
}

func createFIFOWithDB(t *testing.T, db *pgxpool.Pool, id, binding, source, kind string, payload, media json.RawMessage, attachmentBytes int64, expected any) (sqlc.ChannelBindingFifo, error) {
	t.Helper()
	return createChannelBindingFIFO(t.Context(), db, sqlc.CreateChannelBindingFIFOParams{
		ID: id, ChannelID: "fifo-channel", BindingKey: binding, PrincipalID: "principal-1",
		SourceKey: source, Kind: kind, Payload: payload, ImmutableMedia: media,
		AttachmentBytes: attachmentBytes, ExpectedSessionID: expected,
	})
}

func equalFIFOJSON(a, b json.RawMessage) bool {
	var av, bv any
	return json.Unmarshal(a, &av) == nil && json.Unmarshal(b, &bv) == nil && reflect.DeepEqual(av, bv)
}

func TestChannelBindingFIFOStoresOnlyImmutableAttachmentIdentity(t *testing.T) {
	q := newChannelFIFOQueries(t)
	payload := json.RawMessage(`[{"kind":"image_ref","media_id":"61000000-0000-0000-0000-000000000001"}]`)
	media := json.RawMessage(`[{"media_id":"61000000-0000-0000-0000-000000000001","size_bytes":4096}]`)
	row, err := createFIFO(t, q, "10000000-0000-0000-0000-000000000001", "attachment-chat", "delivery-1", "message", payload, media, 4096, nil)
	if err != nil {
		t.Fatal(err)
	}

	// The adapter's source URL may already be expired when a worker reads the
	// envelope. The SQL receipt must be sufficient by itself and must not retain
	// bearer URLs in either durable JSON field.
	got, err := q.GetChannelBindingFIFO(t.Context(), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !equalFIFOJSON(got.Payload, payload) || !equalFIFOJSON(got.ImmutableMedia, media) || got.AttachmentBytes != 4096 {
		t.Fatalf("immutable attachment changed: payload=%s media=%s bytes=%d", got.Payload, got.ImmutableMedia, got.AttachmentBytes)
	}
	if durable := string(got.Payload) + string(got.ImmutableMedia); strings.Contains(durable, "http://") || strings.Contains(durable, "https://") || strings.Contains(durable, "expires") {
		t.Fatalf("durable attachment contains an expiring source reference: %s", durable)
	}

	// Stable redelivery is exact, including canonical media metadata; changing
	// metadata under the same delivery identity fails closed.
	duplicate, err := createFIFO(t, q, "10000000-0000-0000-0000-000000000002", "attachment-chat", "delivery-1", "message", payload, media, 4096, nil)
	if err != nil || duplicate.ID != row.ID {
		t.Fatalf("exact redelivery = (%s, %v), want original %s", duplicate.ID, err, row.ID)
	}
	changedPayload := json.RawMessage(`[{"kind":"image_ref","media_id":"61000000-0000-0000-0000-000000000002"}]`)
	_, err = createFIFO(t, q, "10000000-0000-0000-0000-000000000003", "attachment-chat", "delivery-1", "message", changedPayload, json.RawMessage(`[{"media_id":"61000000-0000-0000-0000-000000000002","size_bytes":4096}]`), 4096, nil)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("changed media metadata collision = %v, want no row", err)
	}
}

func TestChannelBindingFIFORejectsNonCanonicalAttachmentRepresentations(t *testing.T) {
	q := newChannelFIFOQueries(t)
	tests := []struct {
		name       string
		payload    json.RawMessage
		media      json.RawMessage
		attachment int64
	}{
		{"inline base64", json.RawMessage(`[{"kind":"image","data":"aGVsbG8=","mime_type":"image/png"}]`), json.RawMessage(`[]`), 0},
		{"URL-bearing ref", json.RawMessage(`[{"kind":"image_ref","media_id":"62000000-0000-0000-0000-000000000001","url":"https://expired.example/image"}]`), json.RawMessage(`[{"media_id":"62000000-0000-0000-0000-000000000001","size_bytes":5}]`), 5},
		{"non UUID ref", json.RawMessage(`[{"kind":"image_ref","media_id":"sha256:not-an-id"}]`), json.RawMessage(`[{"media_id":"sha256:not-an-id","size_bytes":5}]`), 5},
		{"missing metadata", json.RawMessage(`[{"kind":"image_ref","media_id":"62000000-0000-0000-0000-000000000002"}]`), json.RawMessage(`[]`), 0},
		{"unreferenced metadata", json.RawMessage(`[{"kind":"text","text":"hello"}]`), json.RawMessage(`[{"media_id":"62000000-0000-0000-0000-000000000003","size_bytes":5}]`), 5},
		{"wrong byte total", json.RawMessage(`[{"kind":"image_ref","media_id":"62000000-0000-0000-0000-000000000004"}]`), json.RawMessage(`[{"media_id":"62000000-0000-0000-0000-000000000004","size_bytes":5}]`), 4},
		{"duplicate metadata", json.RawMessage(`[{"kind":"image_ref","media_id":"62000000-0000-0000-0000-000000000005"}]`), json.RawMessage(`[{"media_id":"62000000-0000-0000-0000-000000000005","size_bytes":5},{"media_id":"62000000-0000-0000-0000-000000000005","size_bytes":5}]`), 10},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := fmt.Sprintf("63000000-0000-0000-0000-%012d", i+1)
			if _, err := createFIFO(t, q, id, "canonical-chat", "invalid-"+id, "message", tt.payload, tt.media, tt.attachment, nil); err == nil {
				t.Fatal("non-canonical durable attachment was accepted")
			}
		})
	}
}

func TestChannelBindingFIFORejectsAnotherUsersImmutableMedia(t *testing.T) {
	db := dbtest.New(t)
	q := sqlc.New(db)
	owners := []string{"71000000-0000-0000-0000-000000000001", "71000000-0000-0000-0000-000000000002"}
	for _, owner := range owners {
		if _, err := appdb.NewOIDCStore(db).CreateUser(t.Context(), auth.User{
			ID: owner, Email: owner + "@example.test", Name: owner,
		}); err != nil {
			t.Fatalf("create media owner: %v", err)
		}
	}
	media, err := q.CreateMediaIfAbsent(t.Context(), sqlc.CreateMediaIfAbsentParams{
		UserID: owners[0], Sha256: make([]byte, 32), MimeType: "image/png", SizeBytes: 16,
	})
	if err != nil {
		t.Fatalf("create immutable media: %v", err)
	}
	blocks := []ai.ContentBlock{ai.ImageRefContent{MediaID: media.ID}}
	if _, _, err := immutableMediaMetadataForUser(t.Context(), q, owners[1], blocks); err == nil {
		t.Fatal("another user's immutable media was admitted to the channel FIFO")
	}
	metadata, bytes, err := immutableMediaMetadataForUser(t.Context(), q, owners[0], blocks)
	if err != nil || bytes != 16 || !strings.Contains(string(metadata), media.ID) {
		t.Fatalf("owner immutable media metadata = %s/%d err=%v", metadata, bytes, err)
	}
}

func TestChannelBindingFIFOConcurrentQuotaIsAcceptanceBoundary(t *testing.T) {
	db, q := newChannelFIFODB(t)
	payload := json.RawMessage(`[{"kind":"text","text":"queued"}]`)
	for i := range 127 {
		id := fmt.Sprintf("20000000-0000-0000-0000-%012d", i+1)
		if _, err := createFIFO(t, q, id, "quota-chat", fmt.Sprintf("delivery-%d", i), "message", payload, json.RawMessage(`[]`), 0, nil); err != nil {
			t.Fatalf("seed quota row %d: %v", i, err)
		}
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := createFIFOWithDB(t, db, fmt.Sprintf("30000000-0000-0000-0000-%012d", i+1), "quota-chat", fmt.Sprintf("contender-%d", i), "message", payload, json.RawMessage(`[]`), 0, nil)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	accepted, refused := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, pgx.ErrNoRows):
			refused++
		default:
			t.Fatalf("quota contender: %v", err)
		}
	}
	if accepted != 1 || refused != 1 {
		t.Fatalf("quota contenders: accepted=%d refused=%d, want 1/1", accepted, refused)
	}
	if _, err := q.GetChannelBindingFIFOBySource(t.Context(), sqlc.GetChannelBindingFIFOBySourceParams{ChannelID: "fifo-channel", SourceKey: "never-created"}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("missing receipt lookup = %v", err)
	}
}

func TestChannelBindingFIFOConcurrentByteQuotaIsAtomic(t *testing.T) {
	db, q := newChannelFIFODB(t)
	const mib = int64(1024 * 1024)
	attachment := func(id string, size int64) (json.RawMessage, json.RawMessage) {
		return json.RawMessage(fmt.Sprintf(`[{"kind":"image_ref","media_id":%q}]`, id)),
			json.RawMessage(fmt.Sprintf(`[{"media_id":%q,"size_bytes":%d}]`, id, size))
	}
	// Four 60 MiB rows leave less than two 8 MiB admissions once their exact
	// JSON payload bytes are included. The global advisory admission lock must
	// let one contender observe the other's committed byte usage.
	for i := range 4 {
		mediaID := fmt.Sprintf("64000000-0000-0000-0000-%012d", i+1)
		payload, metadata := attachment(mediaID, 60*mib)
		if _, err := createFIFO(t, q, fmt.Sprintf("65000000-0000-0000-0000-%012d", i+1), "byte-quota-chat", fmt.Sprintf("seed-%d", i), "message", payload, metadata, 60*mib, nil); err != nil {
			t.Fatalf("seed byte quota row %d: %v", i, err)
		}
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			mediaID := fmt.Sprintf("66000000-0000-0000-0000-%012d", i+1)
			payload, metadata := attachment(mediaID, 8*mib)
			_, err := createFIFOWithDB(t, db, fmt.Sprintf("67000000-0000-0000-0000-%012d", i+1), "byte-quota-chat", fmt.Sprintf("contender-%d", i), "message", payload, metadata, 8*mib, nil)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	accepted, refused := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, pgx.ErrNoRows):
			refused++
		default:
			t.Fatalf("byte quota contender: %v", err)
		}
	}
	if accepted != 1 || refused != 1 {
		t.Fatalf("byte quota contenders: accepted=%d refused=%d, want 1/1", accepted, refused)
	}
}

func TestChannelBindingFIFOPrincipalAndDeploymentRowQuotas(t *testing.T) {
	for _, tc := range []struct {
		name, prefix string
		rows         int
		sharedOwner  bool
	}{
		{name: "principal", prefix: "principal-quota-", rows: 512, sharedOwner: true},
		{name: "deployment", prefix: "deployment-quota-", rows: 4096, sharedOwner: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, _ := newChannelFIFODB(t)
			if _, err := db.Exec(t.Context(), `
				INSERT INTO channel_binding_fifo (
					id, channel_id, binding_key, principal_id, source_key, kind,
					payload, immutable_media, payload_bytes, attachment_bytes
				)
				SELECT gen_random_uuid(), 'fifo-channel', $1 || n::text,
				       CASE WHEN $3::boolean THEN 'principal-1' ELSE $1 || 'owner-' || n::text END,
				       $1 || 'source-' || n::text, 'message', '[]'::jsonb, '[]'::jsonb,
				       pg_column_size('[]'::jsonb::text), 0
				FROM generate_series(1, $2::integer) AS n
			`, tc.prefix, tc.rows, tc.sharedOwner); err != nil {
				t.Fatalf("seed %s row quota: %v", tc.name, err)
			}
			_, err := createChannelBindingFIFO(t.Context(), db, sqlc.CreateChannelBindingFIFOParams{
				ID: uuid.NewString(), ChannelID: "fifo-channel", BindingKey: tc.prefix + "contender",
				PrincipalID: "principal-1", SourceKey: tc.prefix + "contender", Kind: "message",
				Payload: json.RawMessage(`[]`), ImmutableMedia: json.RawMessage(`[]`),
			})
			if !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("%s row quota admission = %v, want refusal before ack", tc.name, err)
			}
		})
	}
}

func TestChannelBindingFIFOPrincipalAndDeploymentByteQuotasAreAtomic(t *testing.T) {
	const mib = int64(1024 * 1024)
	for _, tc := range []struct {
		name            string
		seedRows        int
		seedBytes       int64
		contenderBytes  int64
		sharedPrincipal bool
	}{
		{name: "principal", seedRows: 16, seedBytes: 60 * mib, contenderBytes: 32 * mib, sharedPrincipal: true},
		{name: "deployment", seedRows: 136, seedBytes: 60 * mib, contenderBytes: 16 * mib, sharedPrincipal: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, _ := newChannelFIFODB(t)
			insert := func(prefix string, index int, size int64, contender bool) error {
				mediaID := uuid.NewString()
				principal := fmt.Sprintf("%s-owner-%d", prefix, index)
				if tc.sharedPrincipal {
					principal = "principal-byte-quota"
				}
				payload := json.RawMessage(fmt.Sprintf(`[{"kind":"image_ref","media_id":%q}]`, mediaID))
				metadata := json.RawMessage(fmt.Sprintf(`[{"media_id":%q,"size_bytes":%d}]`, mediaID, size))
				_, err := createChannelBindingFIFO(t.Context(), db, sqlc.CreateChannelBindingFIFOParams{
					ID: uuid.NewString(), ChannelID: "fifo-channel",
					BindingKey:  fmt.Sprintf("%s-binding-%d-%t", prefix, index, contender),
					PrincipalID: principal, SourceKey: fmt.Sprintf("%s-source-%d-%t", prefix, index, contender),
					Kind: "message", Payload: payload, ImmutableMedia: metadata, AttachmentBytes: size,
				})
				return err
			}
			for i := 0; i < tc.seedRows; i++ {
				if err := insert(tc.name, i, tc.seedBytes, false); err != nil {
					t.Fatalf("seed %s byte quota %d: %v", tc.name, i, err)
				}
			}
			var wg sync.WaitGroup
			errs := make(chan error, 2)
			for i := range 2 {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					errs <- insert(tc.name+"-contender", i, tc.contenderBytes, true)
				}(i)
			}
			wg.Wait()
			close(errs)
			accepted, refused := 0, 0
			for err := range errs {
				switch {
				case err == nil:
					accepted++
				case errors.Is(err, pgx.ErrNoRows):
					refused++
				default:
					t.Fatalf("%s byte quota contender: %v", tc.name, err)
				}
			}
			if accepted != 1 || refused != 1 {
				t.Fatalf("%s byte quota contenders: accepted=%d refused=%d, want 1/1", tc.name, accepted, refused)
			}
		})
	}
}

func TestStableChannelDeliveryRequiresImmutablePlatformID(t *testing.T) {
	msg := pkgchannel.IncomingMessage{
		Platform: "telegram", SenderID: "sender", Timestamp: time.Unix(1_700_000_000, 123).UTC(),
		Content: []ai.ContentBlock{ai.TextContent{Text: "same delivery"}},
	}
	if id, ok := stableChannelDeliveryID(msg); ok || id != "" {
		t.Fatalf("unverifiable delivery identity = %q/%v, want rejection", id, ok)
	}
	msg.MessageID = "platform-message-1"
	if id, ok := stableChannelDeliveryID(msg); !ok || id != msg.MessageID {
		t.Fatalf("platform delivery identity = %q/%v, want %q", id, ok, msg.MessageID)
	}
}

func TestChannelBindingFIFONewCommandsRetainOrderedSessionSnapshots(t *testing.T) {
	q := newChannelFIFOQueries(t)
	first, err := createFIFO(t, q, "40000000-0000-0000-0000-000000000001", "new-chat", "command:new:1", "new", json.RawMessage(`[]`), json.RawMessage(`[]`), 0, pgtype.Text{String: "session-old", Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := createFIFO(t, q, "40000000-0000-0000-0000-000000000002", "new-chat", "command:new:2", "new", json.RawMessage(`[]`), json.RawMessage(`[]`), 0, pgtype.Text{String: "session-old", Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	if second.BindingRevision != first.BindingRevision+1 || second.EnqueueSeq.Int64 <= first.EnqueueSeq.Int64 {
		t.Fatalf("/new order: first=(%d,%d) second=(%d,%d)", first.BindingRevision, first.EnqueueSeq.Int64, second.BindingRevision, second.EnqueueSeq.Int64)
	}
	claimed, err := q.ClaimChannelBindingFIFOHead(t.Context(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.ClaimChannelBindingFIFOHead(t.Context(), second.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("successor /new overtook head: %v", err)
	}
	if rows, err := q.CompleteChannelBindingFIFOControl(t.Context(), sqlc.CompleteChannelBindingFIFOControlParams{ID: first.ID, ClaimToken: claimed.ClaimToken}); err != nil || rows != 1 {
		t.Fatalf("complete first /new: rows=%d err=%v", rows, err)
	}
	staleSuccessor, err := q.ClaimChannelBindingFIFOHead(t.Context(), second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if staleSuccessor.ExpectedSessionID.String != "session-old" {
		t.Fatalf("successor expected session = %q, want frozen session-old", staleSuccessor.ExpectedSessionID.String)
	}
}
