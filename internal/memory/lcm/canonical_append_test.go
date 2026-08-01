package lcm_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/lcm"
	"github.com/CherryHQ/stella/pkg/ai"
)

const encodedPixels = "QklOQVJZX1BJWEVMU19NVVNUX05PVF9CRV9TVE9SRUQ="

func TestAppendCanonicalStoresBaselineProjectionAndParts(t *testing.T) {
	db := newLCMTestDB(t)
	defer db.Close()
	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	ctx := context.Background()
	sess := memory.Session{ID: "canonical-storage", UserID: testUserID, AgentID: "test", Channel: "test"}
	mediaID := insertCanonicalTestMedia(t, db, testUserID, "image/png")
	baseline := testBaseline("baseline-only-searchable-word")
	ref := canonicalTestRef(mediaID, "image/png", baseline)
	if err := p.AppendCanonical(ctx, sess, ai.UserMessage{Content: []ai.ContentBlock{
		ai.TextContent{Text: "please inspect"}, ref,
	}}); err != nil {
		t.Fatalf("AppendCanonical: %v", err)
	}

	var eventType, content string
	var tokens int64
	if err := db.QueryRow(ctx, `SELECT event_type, content, token_count FROM ctx_message`).Scan(&eventType, &content, &tokens); err != nil {
		t.Fatal(err)
	}
	if eventType != "multimodal" {
		t.Fatalf("image-bearing parent event_type = %q, want multimodal", eventType)
	}
	wantProjection := "please inspect " + baseline
	if content != wantProjection {
		t.Fatalf("parent content = %q, want %q", content, wantProjection)
	}
	if tokens != int64(memory.EstimateTokens(wantProjection)) {
		t.Fatalf("token_count = %d, want projection tokens %d", tokens, memory.EstimateTokens(wantProjection))
	}
	if strings.Contains(content, encodedPixels) {
		t.Fatal("base64 entered ctx_message.content")
	}

	var partText strings.Builder
	var statuses []string
	rows, err := db.Query(ctx, `SELECT COALESCE(text_content, ''), baseline_status FROM ctx_message_part ORDER BY ordinal`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var text, status string
		if err := rows.Scan(&text, &status); err != nil {
			t.Fatal(err)
		}
		partText.WriteString(text)
		statuses = append(statuses, status)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(partText.String(), encodedPixels) {
		t.Fatal("base64 entered ctx_message_part")
	}
	if got, want := statuses, []string{"", "ready"}; len(got) != len(want) || got[1] != want[1] {
		t.Fatalf("part baseline statuses = %v, want %v", got, want)
	}

	// An old reader that ignores child rows still receives readable parent text.
	if !strings.Contains(content, baseline) {
		t.Fatalf("old-reader parent projection lost baseline: %q", content)
	}

	readCtx := authz.WithAgentID(authz.WithUserID(ctx, testUserID), "test")
	history, err := p.LoadHistory(readCtx, sess.ID)
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history length = %d", len(history))
	}
	user, ok := history[0].(ai.UserMessage)
	if !ok {
		t.Fatalf("history message = %T", history[0])
	}
	blocks := user.Content.([]ai.ContentBlock)
	if len(blocks) != 2 {
		t.Fatalf("reconstructed blocks = %#v", blocks)
	}
	if got, ok := blocks[1].(ai.ImageRefContent); !ok || got.MediaID != mediaID || got.Baseline.Text != baseline {
		t.Fatalf("parts-first reconstruction = %#v", blocks[1])
	}

	results, err := p.Search(ctx, sess, memory.SearchQuery{Text: "baseline-only-searchable-word", Scope: memory.SearchScopeMessages})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].SourceID == "" {
		t.Fatalf("baseline-only word was not searchable: %#v", results)
	}
}

func TestAppendKeepsLegacyInlineImageStorage(t *testing.T) {
	db := newLCMTestDB(t)
	defer db.Close()
	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	sess := memory.Session{ID: "legacy-image-append", UserID: testUserID, AgentID: "test", Channel: "test"}
	if err := p.Append(context.Background(), sess, ai.UserMessage{Content: []ai.ContentBlock{
		ai.ImageContent{Data: encodedPixels, MimeType: "image/png"},
	}}); err != nil {
		t.Fatalf("legacy Append: %v", err)
	}
	var content string
	if err := db.QueryRow(context.Background(), `SELECT content FROM ctx_message`).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, encodedPixels) {
		t.Fatalf("legacy Append stopped preserving inline image content: %q", content)
	}
}

func TestAppendCanonicalTextOnlyWritesNoPartsAndRoundTrips(t *testing.T) {
	db := newLCMTestDB(t)
	defer db.Close()
	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	ctx := context.Background()
	sess := memory.Session{ID: "canonical-text-only", UserID: testUserID, AgentID: "test", Channel: "test"}
	if err := p.AppendCanonical(ctx, sess,
		ai.UserMessage{Content: "plain user"},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.ToolCall{ID: "plain-call", Name: "echo"}}},
		ai.ToolResultMessage{ToolCallID: "plain-call", ToolName: "echo", Content: []ai.ContentBlock{ai.TextContent{Text: "plain tool"}}},
	); err != nil {
		t.Fatalf("AppendCanonical: %v", err)
	}
	var parts int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM ctx_message_part`).Scan(&parts); err != nil {
		t.Fatal(err)
	}
	if parts != 0 {
		t.Fatalf("text-only canonical messages wrote %d parts", parts)
	}
	var userEventType string
	if err := db.QueryRow(ctx, `SELECT event_type FROM ctx_message WHERE role = 'user'`).Scan(&userEventType); err != nil {
		t.Fatal(err)
	}
	if userEventType != "text" {
		t.Fatalf("text-only user event_type = %q, want text", userEventType)
	}

	readCtx := authz.WithAgentID(authz.WithUserID(ctx, testUserID), "test")
	history, err := p.LoadHistory(readCtx, sess.ID)
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("history length = %d, want 3", len(history))
	}
	if user, ok := history[0].(ai.UserMessage); !ok || user.Content != "plain user" {
		t.Fatalf("user round-trip = %#v", history[0])
	}
	if tool, ok := history[2].(ai.ToolResultMessage); !ok || ai.FlattenText(tool.Content) != "plain tool" {
		t.Fatalf("tool round-trip = %#v", history[2])
	}

	if err := p.AppendCanonical(ctx, sess, ai.UserMessage{Content: ""}); err != nil {
		t.Fatalf("empty canonical user append: %v", err)
	}
	var messages int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM ctx_message`).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if messages != 3 {
		t.Fatalf("empty canonical user wrote a message: %d rows", messages)
	}
}

func TestAppendCanonicalEmptyToolErrorRoundTrips(t *testing.T) {
	db := newLCMTestDB(t)
	defer db.Close()
	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	ctx := context.Background()
	sess := memory.Session{ID: "canonical-empty-tool-error", UserID: testUserID, AgentID: "test", Channel: "test"}
	if err := p.AppendCanonical(ctx, sess, ai.ToolResultMessage{ToolCallID: "empty-error", ToolName: "bash", IsError: true}); err != nil {
		t.Fatalf("AppendCanonical: %v", err)
	}
	readCtx := authz.WithAgentID(authz.WithUserID(ctx, testUserID), "test")
	history, err := p.LoadHistory(readCtx, sess.ID)
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history length = %d", len(history))
	}
	tool, ok := history[0].(ai.ToolResultMessage)
	if !ok || !tool.IsError || len(tool.Content) != 1 || ai.FlattenText(tool.Content) != "" {
		t.Fatalf("canonical empty error round-trip = %#v", history[0])
	}
}

func TestAppendCanonicalRejectsRawImageWithoutWriting(t *testing.T) {
	db := newLCMTestDB(t)
	defer db.Close()
	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	sess := memory.Session{ID: "canonical-reject", UserID: testUserID, AgentID: "test", Channel: "test"}
	err = p.AppendCanonical(context.Background(), sess, ai.UserMessage{Content: []ai.ContentBlock{
		ai.ImageContent{Data: encodedPixels, MimeType: "image/png"},
	}})
	if !errors.Is(err, ai.ErrRawImageContent) {
		t.Fatalf("AppendCanonical error = %v, want raw-image rejection", err)
	}
	var count int
	if err := db.QueryRow(context.Background(), `SELECT COUNT(*) FROM ctx_message`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("raw image append wrote %d messages", count)
	}
}

func TestAppendCanonicalRejectsForeignMediaAndMIMEMismatch(t *testing.T) {
	db := newLCMTestDB(t)
	defer db.Close()
	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	ctx := context.Background()
	sess := memory.Session{ID: "canonical-media-scope", UserID: testUserID, AgentID: "test", Channel: "test"}
	foreignMedia := insertCanonicalTestMedia(t, db, testOtherUserID, "image/png")
	ownedMedia := insertCanonicalTestMedia(t, db, testUserID, "image/png")
	for _, tc := range []struct {
		name string
		ref  ai.ImageRefContent
	}{
		{name: "foreign", ref: canonicalTestRef(foreignMedia, "image/png", testBaseline("foreign"))},
		{name: "mime mismatch", ref: canonicalTestRef(ownedMedia, "image/jpeg", testBaseline("mismatch"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := p.AppendCanonical(ctx, sess, ai.UserMessage{Content: []ai.ContentBlock{tc.ref}})
			if err == nil || err.Error() != "canonical media unavailable" {
				t.Fatalf("AppendCanonical error = %v, want opaque media failure", err)
			}
			var count int
			if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM ctx_message`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("rejected media wrote %d messages", count)
			}
		})
	}
}

func TestAppendCanonicalInvalidMediaRollsBackMultipleRows(t *testing.T) {
	db := newLCMTestDB(t)
	defer db.Close()
	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	ctx := context.Background()
	sess := memory.Session{ID: "canonical-atomic", UserID: testUserID, AgentID: "test", Channel: "test"}
	validMedia := insertCanonicalTestMedia(t, db, testUserID, "image/png")
	foreignMedia := insertCanonicalTestMedia(t, db, testOtherUserID, "image/png")
	err = p.AppendCanonical(ctx, sess,
		ai.UserMessage{Content: []ai.ContentBlock{canonicalTestRef(validMedia, "image/png", testBaseline("first row"))}},
		ai.UserMessage{Content: []ai.ContentBlock{canonicalTestRef(foreignMedia, "image/png", testBaseline("second row"))}},
	)
	if err == nil || err.Error() != "canonical media unavailable" {
		t.Fatalf("AppendCanonical error = %v, want opaque media failure", err)
	}
	for _, table := range []string{"ctx_message", "ctx_message_part"} {
		var count int
		if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s retained %d rows after multi-row failure", table, count)
		}
	}
}

func TestAppendCanonicalRejectsAssistantImages(t *testing.T) {
	db := newLCMTestDB(t)
	defer db.Close()
	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	mediaID := insertCanonicalTestMedia(t, db, testUserID, "image/png")
	sess := memory.Session{ID: "canonical-assistant-image", UserID: testUserID, AgentID: "test", Channel: "test"}
	for _, tc := range []struct {
		name  string
		block ai.ContentBlock
		want  error
	}{
		{name: "raw", block: ai.ImageContent{Data: encodedPixels, MimeType: "image/png"}, want: ai.ErrRawImageContent},
		{name: "reference", block: canonicalTestRef(mediaID, "image/png", testBaseline("assistant ref")), want: ai.ErrUnsupportedCanonicalBlock},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := p.AppendCanonical(context.Background(), sess, ai.AssistantMessage{Content: []ai.ContentBlock{tc.block}})
			if !errors.Is(err, tc.want) {
				t.Fatalf("AppendCanonical error = %v, want %v", err, tc.want)
			}
		})
	}
	var count int
	if err := db.QueryRow(context.Background(), `SELECT COUNT(*) FROM ctx_message`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("assistant image rejection wrote %d messages", count)
	}
}

func TestCanonicalCompactionUsesUserAndToolBaselinesOnly(t *testing.T) {
	db := newLCMTestDB(t)
	defer db.Close()
	var (
		mu      sync.Mutex
		prompts []string
	)
	p, err := lcm.New(db, func(_ context.Context, prompt string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		prompts = append(prompts, prompt)
		return "summary", nil
	}, map[string]any{"fresh_tail": 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	ctx := context.Background()
	sess := memory.Session{ID: "canonical-compaction", UserID: testUserID, AgentID: "test", Channel: "test"}
	userMedia := insertCanonicalTestMedia(t, db, testUserID, "image/png")
	toolMedia := insertCanonicalTestMedia(t, db, testUserID, "image/jpeg")
	if err := p.AppendCanonical(ctx, sess,
		ai.UserMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "user text"}, canonicalTestRef(userMedia, "image/png", testBaseline("USER_BASELINE_ONLY"))}},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.ToolCall{ID: "call", Name: "read"}}},
		ai.ToolResultMessage{ToolCallID: "call", ToolName: "read", Content: []ai.ContentBlock{ai.TextContent{Text: "tool text"}, canonicalTestRef(toolMedia, "image/jpeg", testBaseline("TOOL_BASELINE_ONLY"))}},
	); err != nil {
		t.Fatalf("append canonical image turn: %v", err)
	}
	for i := range 8 {
		if err := p.AppendCanonical(ctx, sess, ai.UserMessage{Content: "filler"}); err != nil {
			t.Fatalf("append filler %d: %v", i, err)
		}
	}

	result, err := memory.Compactor(p).Compact(ctx, sess, memory.CompactionIncremental)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if result.LeafSummariesCreated != 1 {
		t.Fatalf("leaf summaries = %d, want 1", result.LeafSummariesCreated)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(prompts) == 0 {
		t.Fatal("summarizer was not called")
	}
	joined := strings.Join(prompts, "\n")
	for _, want := range []string{"USER_BASELINE_ONLY", "TOOL_BASELINE_ONLY"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("summarizer input missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, encodedPixels) {
		t.Fatal("base64 reached the summarizer")
	}
}

func testBaseline(word string) string {
	return "## Text\n" + word + "\n\n## Scene\na test scene"
}

func canonicalTestRef(mediaID, mimeType, baseline string) ai.ImageRefContent {
	return ai.ImageRefContent{
		MediaID:  mediaID,
		MimeType: mimeType,
		Baseline: ai.ImageBaseline{Status: ai.ImageBaselineReady, Text: baseline, Renderer: "test/model", Contract: ai.ImageBaselineContractV1},
	}
}

func insertCanonicalTestMedia(t *testing.T, db *pgxpool.Pool, userID, mimeType string) string {
	t.Helper()
	id := uuid.NewString()
	hash := make([]byte, 32)
	copy(hash, id)
	if _, err := db.Exec(context.Background(), `
		INSERT INTO ctx_media (id, user_id, sha256, mime_type, size_bytes, width_px, height_px)
		VALUES ($1, $2, $3, $4, 1, 1, 1)
	`, id, userID, hash, mimeType); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	return id
}
