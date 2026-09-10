package channel

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/agentrun"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/renderrefs"
)

func TestFileHandoffPersistsBytesAndSendsAnImmutableSnapshot(t *testing.T) {
	f := newDeliveryFixture(t)
	delivery := f.newDelivery(t)
	path := filepath.Join(t.TempDir(), "result.bin")
	original := bytes.Repeat([]byte("1234567"), 400000)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	event, err := delivery.persistEvent(t.Context(), pkgchannel.Event{File: &pkgchannel.FileEvent{Path: path, Name: "report.bin"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed after the turn"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(event.File.Path)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("adapter snapshot changed: bytes=%d err=%v", len(got), err)
	}
	var stored []byte
	rows, err := f.db.Query(t.Context(), `SELECT metadata, data FROM agent_run_output_part WHERE run_id = $1 ORDER BY event_no, chunk_no`, f.lease.Guard.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var metadata, chunk []byte
		if err := rows.Scan(&metadata, &chunk); err != nil {
			t.Fatal(err)
		}
		var part outputPartMetadata
		if err := json.Unmarshal(metadata, &part); err != nil {
			t.Fatal(err)
		}
		if part.Kind != "file" || part.Name != "report.bin" || len(chunk) > 1<<20 {
			t.Fatalf("wrong part metadata=%+v size=%d", part, len(chunk))
		}
		stored = append(stored, chunk...)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, original) {
		t.Fatal("durable file content changed or lost chunks")
	}
	outcome := agentruntime.TurnOutcome{Status: agentrun.StatusCompleted}
	if err := commitTurnHandoff(t.Context(), f.lease, outcome, deliverableTurn(outcome, f.target, delivery)); err != nil {
		t.Fatal(err)
	}
	if err := delivery.Authorize(t.Context(), pkgchannel.SendOutput); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Acquire(t.Context(), f.lease.Guard.SessionID, "next-turn"); err != nil {
		t.Fatalf("unsent attachment retained execution ownership: %v", err)
	}
	if err := delivery.Settle(t.Context(), pkgchannel.DeliverySent); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(event.File.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("settled delivery retained temporary snapshot: %v", err)
	}
}

func TestImageAndReferenceHandoffRetainsContentAndOrdering(t *testing.T) {
	f := newDeliveryFixture(t)
	delivery := f.newDelivery(t)
	image := []byte("image content")
	event := pkgchannel.Event{
		Image:      &pkgchannel.ImageEvent{Data: base64.StdEncoding.EncodeToString(image), MimeType: "image/png"},
		References: []renderrefs.Reference{{V: 1, Type: "goal", ID: "goal-1"}},
	}
	if _, err := delivery.persistEvent(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	rows, err := f.db.Query(t.Context(), `SELECT metadata, data FROM agent_run_output_part WHERE run_id = $1 ORDER BY event_no, chunk_no`, f.lease.Guard.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for i, kind := range []string{"image", "references"} {
		if !rows.Next() {
			t.Fatalf("missing output part %d: %v", i, rows.Err())
		}
		var metadata, data []byte
		if err := rows.Scan(&metadata, &data); err != nil {
			t.Fatal(err)
		}
		var part outputPartMetadata
		if err := json.Unmarshal(metadata, &part); err != nil {
			t.Fatal(err)
		}
		if part.Kind != kind {
			t.Fatalf("part %d kind = %s", i, part.Kind)
		}
		if kind == "image" && (!bytes.Equal(data, image) || part.MimeType != "image/png") {
			t.Fatal("image bytes or MIME changed")
		}
		if kind == "references" && (len(part.References) != 1 || part.References[0].ID != "goal-1") {
			t.Fatal("reference identifiers were lost")
		}
	}
}

func TestExpiredRunCannotPersistRichOutput(t *testing.T) {
	f := newDeliveryFixture(t)
	delivery := f.newDelivery(t)
	if _, err := f.db.Exec(t.Context(), `UPDATE agent_run SET lease_expires_at = clock_timestamp() - interval '1 second' WHERE id = $1`, f.lease.Guard.RunID); err != nil {
		t.Fatal(err)
	}
	event := pkgchannel.Event{Image: &pkgchannel.ImageEvent{Data: "Ynl0ZXM=", MimeType: "image/png"}}
	if _, err := delivery.persistEvent(t.Context(), event); !errors.Is(err, agentrun.ErrLeaseLost) {
		t.Fatalf("stale output persistence = %v", err)
	}
	var count int
	if err := f.db.QueryRow(t.Context(), "SELECT count(*) FROM agent_run_output_part WHERE run_id = $1", f.lease.Guard.RunID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("stale execution committed an attachment")
	}
}
