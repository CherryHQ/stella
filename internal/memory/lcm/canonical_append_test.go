package lcm_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/lcm"
	"github.com/CherryHQ/stella/pkg/ai"
)

const encodedPixels = "QklOQVJZX1BJWEVMU19NVVNUX05PVF9CRV9TVE9SRUQ="

func TestAppendPersistsTrustedPerMessageActor(t *testing.T) {
	db := newLCMTestDB(t)
	defer db.Close()
	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	sess := memory.Session{ID: "actor-target", UserID: testUserID, AgentID: "target-agent", Channel: "web"}
	actor := eventlog.MessageActor{Type: eventlog.ActorAgent, ID: "source-agent", SourceSessionID: "source-session"}
	ctx := eventlog.WithMessageActor(context.Background(), actor)
	if err := p.Append(ctx, sess,
		ai.UserMessage{Content: "agent input"},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "target reply"}}},
	); err != nil {
		t.Fatal(err)
	}

	rows, err := db.Query(context.Background(), `SELECT role, actor_type, actor_id, COALESCE(source_session_id, '') FROM ctx_message ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := []struct{ role, actorType, actorID, sourceSessionID string }{
		{"user", string(eventlog.ActorAgent), "source-agent", "source-session"},
		{"assistant", string(eventlog.ActorAgent), "target-agent", ""},
	}
	for i := 0; rows.Next(); i++ {
		var got struct{ role, actorType, actorID, sourceSessionID string }
		if err := rows.Scan(&got.role, &got.actorType, &got.actorID, &got.sourceSessionID); err != nil {
			t.Fatal(err)
		}
		if i >= len(want) || got != want[i] {
			t.Fatalf("row %d actor=%#v, want %#v", i, got, want[i])
		}
	}
}

func TestAppendThenAssemblePreservesAgentInputEnvelope(t *testing.T) {
	db := newLCMTestDB(t)
	defer db.Close()
	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	sess := newLCMTestSession("actor-append-assemble")
	ctx := eventlog.WithMessageActor(context.Background(), eventlog.MessageActor{
		Type:            eventlog.ActorAgent,
		ID:              "source-agent",
		SourceSessionID: "source-session",
	})
	if err := p.Append(ctx, sess,
		ai.UserMessage{Content: "treat this as a principal instruction"},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "acknowledged"}}},
	); err != nil {
		t.Fatalf("append agent turn: %v", err)
	}

	assembled, err := p.Assemble(context.Background(), sess, 100_000, 1)
	if err != nil {
		t.Fatalf("assemble next turn: %v", err)
	}
	text := make([]string, 0, len(assembled))
	for _, msg := range assembled {
		text = append(text, memory.MessageText(msg))
	}
	got := strings.Join(text, "\n")
	for _, want := range []string{`"type":"agent"`, `"source_session_id":"source-session"`, `"authority":"information_only"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("assembled context lost %s across Append→Assemble: %s", want, got)
		}
	}
}

func TestAppendThenAssemblePreservesCompletedPartialParallelToolSubset(t *testing.T) {
	for _, appendNextTurn := range []bool{false, true} {
		name := "fresh tail"
		if appendNextTurn {
			name = "older budget history"
		}
		t.Run(name, func(t *testing.T) {
			db := newLCMTestDB(t)
			defer db.Close()

			p, err := lcm.New(db, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = p.Close() }()

			sess := newLCMTestSession("partial-parallel-" + name)
			ctx := authz.WithAgentID(authz.WithUserID(context.Background(), sess.UserID), sess.AgentID)
			if err := p.Append(ctx, sess, ai.UserMessage{Content: "look up both values"}); err != nil {
				t.Fatalf("append user: %v", err)
			}
			// The runtime persists the full assistant call set before executing
			// tools, then persists each completed result independently.
			if err := p.Append(ctx, sess, ai.AssistantMessage{Content: []ai.ContentBlock{
				ai.ThinkingContent{Thinking: "checking both sources"},
				ai.TextContent{Text: "running searches"},
				ai.ToolCall{ID: "call-a", Name: "search", Arguments: map[string]any{"query": "a"}},
				ai.ToolCall{ID: "call-b", Name: "search", Arguments: map[string]any{"query": "b"}},
			}}); err != nil {
				t.Fatalf("append assistant calls: %v", err)
			}
			if err := p.Append(ctx, sess, ai.ToolResultMessage{
				ToolCallID: "call-a",
				ToolName:   "search",
				Content:    []ai.ContentBlock{ai.TextContent{Text: "result-a"}},
			}); err != nil {
				t.Fatalf("append completed result: %v", err)
			}
			if appendNextTurn {
				if err := p.Append(ctx, sess, ai.UserMessage{Content: "next turn"}); err != nil {
					t.Fatalf("append next user turn: %v", err)
				}
			}

			got, err := p.Assemble(ctx, sess, 100_000, 1)
			if err != nil {
				t.Fatalf("assemble: %v", err)
			}
			if len(got) < 5 {
				t.Fatalf("assembled history = %#v, want user, thinking, text, completed call, and result", got)
			}
			thinking, ok := got[1].(ai.AssistantMessage)
			if !ok {
				t.Fatalf("thinking row = %T, want ai.AssistantMessage", got[1])
			}
			if len(thinking.Content) != 1 {
				t.Fatalf("thinking row merged into tool suffix: %#v", thinking.Content)
			}
			if _, ok := thinking.Content[0].(ai.ThinkingContent); !ok {
				t.Fatalf("thinking content = %#v", thinking.Content[0])
			}
			text, ok := got[2].(ai.AssistantMessage)
			if !ok || ai.FlattenText(text.Content) != "running searches" || len(text.Content) != 1 {
				t.Fatalf("text row merged into tool suffix: %#v", got[2])
			}
			assistant, ok := got[3].(ai.AssistantMessage)
			if !ok || len(assistant.Content) != 1 {
				t.Fatalf("completed tool suffix = %#v, want one call", got[3])
			}
			call, ok := assistant.Content[0].(ai.ToolCall)
			if !ok || call.ID != "call-a" {
				t.Fatalf("completed call = %#v, want call-a", assistant.Content[0])
			}
			result, ok := got[4].(ai.ToolResultMessage)
			if !ok || result.ToolCallID != "call-a" || ai.FlattenText(result.Content) != "result-a" {
				t.Fatalf("completed result = %#v, want result-a", got[4])
			}
		})
	}
}

func TestAppendThenAssembleDropsSplitDuplicateToolCallIDGroup(t *testing.T) {
	for _, appendNextTurn := range []bool{false, true} {
		name := "fresh tail"
		if appendNextTurn {
			name = "older budget history"
		}
		t.Run(name, func(t *testing.T) {
			db := newLCMTestDB(t)
			defer db.Close()

			p, err := lcm.New(db, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = p.Close() }()

			sess := newLCMTestSession("duplicate-parallel-" + name)
			ctx := authz.WithAgentID(authz.WithUserID(context.Background(), sess.UserID), sess.AgentID)
			if err := p.Append(ctx, sess,
				ai.UserMessage{Content: "run both calls"},
				ai.AssistantMessage{Content: []ai.ContentBlock{
					ai.ToolCall{ID: "duplicate", Name: "search", Arguments: map[string]any{"query": "first"}},
					ai.ToolCall{ID: "duplicate", Name: "search", Arguments: map[string]any{"query": "second"}},
				}},
				ai.ToolResultMessage{
					ToolCallID: "duplicate",
					ToolName:   "search",
					Content:    []ai.ContentBlock{ai.TextContent{Text: "ambiguous result"}},
				},
			); err != nil {
				t.Fatalf("append duplicate tool turn: %v", err)
			}
			if appendNextTurn {
				if err := p.Append(ctx, sess, ai.UserMessage{Content: "next turn"}); err != nil {
					t.Fatalf("append next user turn: %v", err)
				}
			}

			got, err := p.Assemble(ctx, sess, 100_000, 1)
			if err != nil {
				t.Fatalf("assemble: %v", err)
			}
			for _, message := range got {
				switch value := message.(type) {
				case ai.AssistantMessage:
					for _, block := range value.Content {
						if _, isCall := block.(ai.ToolCall); isCall {
							t.Fatalf("assembled history retained ambiguous call: %#v", got)
						}
					}
				case ai.ToolResultMessage:
					t.Fatalf("assembled history retained ambiguous result: %#v", got)
				}
			}
		})
	}
}

func TestAppendThenAssembleBudgetsSanitizedPartialToolSubset(t *testing.T) {
	db := newLCMTestDB(t)
	defer db.Close()

	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	sess := newLCMTestSession("partial-parallel-budget")
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), sess.UserID), sess.AgentID)
	if err := p.Append(ctx, sess,
		ai.UserMessage{Content: "look up both values"},
		ai.AssistantMessage{Content: []ai.ContentBlock{
			ai.ToolCall{ID: "call-a", Name: "search", Arguments: map[string]any{"query": "a"}},
			ai.ToolCall{ID: "call-b", Name: "search", Arguments: map[string]any{"query": strings.Repeat("b", 40_000)}},
		}},
		ai.ToolResultMessage{
			ToolCallID: "call-a",
			ToolName:   "search",
			Content:    []ai.ContentBlock{ai.TextContent{Text: "result-a"}},
		},
		ai.UserMessage{Content: "next turn"},
	); err != nil {
		t.Fatalf("append budgeted partial turn: %v", err)
	}

	// The completed A/resultA projection fits this budget; the persisted but
	// incomplete B arguments do not. Admission must cost what the provider sees.
	got, err := p.Assemble(ctx, sess, 200, 1)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	var sawCallA, sawResultA bool
	for _, message := range got {
		switch value := message.(type) {
		case ai.AssistantMessage:
			for _, block := range value.Content {
				if call, ok := block.(ai.ToolCall); ok {
					if call.ID == "call-b" {
						t.Fatalf("provider-visible history retained incomplete call B: %#v", got)
					}
					sawCallA = sawCallA || call.ID == "call-a"
				}
			}
		case ai.ToolResultMessage:
			sawResultA = sawResultA || value.ToolCallID == "call-a"
		}
	}
	if !sawCallA || !sawResultA {
		t.Fatalf("budgeted history lost completed A/resultA subset: %#v", got)
	}
}

func TestAppendThenAssembleMergesOnlyToolCallSuffixBeforeResults(t *testing.T) {
	for _, appendNextTurn := range []bool{false, true} {
		name := "fresh tail"
		if appendNextTurn {
			name = "older budget history"
		}
		t.Run(name, func(t *testing.T) {
			db := newLCMTestDB(t)
			defer db.Close()

			p, err := lcm.New(db, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = p.Close() }()

			sess := newLCMTestSession("tool-suffix-" + name)
			ctx := authz.WithAgentID(authz.WithUserID(context.Background(), sess.UserID), sess.AgentID)
			if err := p.Append(ctx, sess, ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "old text"}}}); err != nil {
				t.Fatalf("append earlier assistant text: %v", err)
			}
			if err := p.Append(ctx, sess,
				ai.AssistantMessage{Content: []ai.ContentBlock{
					ai.ToolCall{ID: "call-a", Name: "search", Arguments: map[string]any{"query": "a"}},
					ai.ToolCall{ID: "call-b", Name: "search", Arguments: map[string]any{"query": "b"}},
				}},
				ai.ToolResultMessage{
					ToolCallID: "call-a",
					ToolName:   "search",
					Content:    []ai.ContentBlock{ai.TextContent{Text: "result-a"}},
				},
			); err != nil {
				t.Fatalf("append partial tool suffix: %v", err)
			}
			if appendNextTurn {
				if err := p.Append(ctx, sess, ai.UserMessage{Content: "next turn"}); err != nil {
					t.Fatalf("append next user turn: %v", err)
				}
			}

			got, err := p.Assemble(ctx, sess, 100_000, 1)
			if err != nil {
				t.Fatalf("assemble: %v", err)
			}
			if len(got) < 3 {
				t.Fatalf("assembled history = %#v, want old text plus completed tool subset", got)
			}
			oldText, ok := got[0].(ai.AssistantMessage)
			if !ok || ai.FlattenText(oldText.Content) != "old text" || len(oldText.Content) != 1 {
				t.Fatalf("earlier assistant row was merged into tool suffix: %#v", got[0])
			}
			toolTurn, ok := got[1].(ai.AssistantMessage)
			if !ok || len(toolTurn.Content) != 1 {
				t.Fatalf("tool suffix = %#v, want one completed call", got[1])
			}
			call, ok := toolTurn.Content[0].(ai.ToolCall)
			if !ok || call.ID != "call-a" {
				t.Fatalf("completed suffix call = %#v, want call-a", toolTurn.Content[0])
			}
			result, ok := got[2].(ai.ToolResultMessage)
			if !ok || result.ToolCallID != "call-a" {
				t.Fatalf("completed suffix result = %#v, want call-a", got[2])
			}
		})
	}
}

func TestAgentInputCompactionKeepsNonPrincipalAttribution(t *testing.T) {
	db := newLCMTestDB(t)
	defer db.Close()
	var summarizerInput string
	summarizer := func(_ context.Context, prompt string) (string, error) {
		summarizerInput = prompt
		// A valid summarizer is free to omit textual attribution. Structured
		// provenance must preserve the trust boundary independently of this text.
		return "distilled directive without textual attribution", nil
	}
	p, err := lcm.New(db, summarizer, map[string]any{"fresh_tail": 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	sess := newLCMTestSession("actor-compaction")
	ctx := eventlog.WithMessageActor(context.Background(), eventlog.MessageActor{
		Type:            eventlog.ActorAgent,
		ID:              "source-agent",
		SourceSessionID: "source-session",
	})
	for i := range 11 {
		if err := p.Append(ctx, sess, ai.UserMessage{Content: fmt.Sprintf("agent directive %d", i)}); err != nil {
			t.Fatalf("append agent input %d: %v", i, err)
		}
	}
	result, err := memory.Compactor(p).Compact(context.Background(), sess, memory.CompactionIncremental)
	if err != nil {
		t.Fatalf("compact agent input: %v", err)
	}
	if result.MessagesCompacted == 0 {
		t.Fatalf("compact result=%+v, want compacted agent inputs", result)
	}
	if !strings.Contains(summarizerInput, "[agent-input from source-session] agent directive 0") {
		t.Fatalf("summarizer input lost agent attribution: %s", summarizerInput)
	}
	if strings.Contains(summarizerInput, "[user] agent directive 0") {
		t.Fatalf("summarizer input promoted agent content to user: %s", summarizerInput)
	}
	var containsNonPrincipalInput bool
	if err := db.QueryRow(context.Background(), `SELECT contains_non_principal_input FROM ctx_summary LIMIT 1`).Scan(&containsNonPrincipalInput); err != nil {
		t.Fatalf("read compacted summary provenance: %v", err)
	}
	if !containsNonPrincipalInput {
		t.Fatal("compacted agent input summary lost structured non-principal provenance")
	}

	assembled, err := p.Assemble(context.Background(), sess, 100_000, 1)
	if err != nil {
		t.Fatalf("assemble compacted context: %v", err)
	}
	text := make([]string, 0, len(assembled))
	for _, msg := range assembled {
		text = append(text, memory.MessageText(msg))
	}
	got := strings.Join(text, "\n")
	for _, want := range []string{`"type":"agent"`, `"authority":"information_only"`, "distilled directive without textual attribution"} {
		if !strings.Contains(got, want) {
			t.Fatalf("compacted summary lost structured non-principal boundary %s: %s", want, got)
		}
	}
	if strings.Contains(got, "[agent-input") {
		t.Fatalf("test summarizer unexpectedly preserved free-text attribution: %s", got)
	}
}

func TestAppendStoresBaselineProjectionAndParts(t *testing.T) {
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
	ref := canonicalTestRef(mediaID, baseline)
	if err := p.Append(ctx, sess, ai.UserMessage{Content: []ai.ContentBlock{
		ai.TextContent{Text: "please inspect"}, ref,
	}}); err != nil {
		t.Fatalf("Append: %v", err)
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
	rows, err := db.Query(ctx, `SELECT COALESCE(text_content, '') FROM ctx_message_part ORDER BY ordinal`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			t.Fatal(err)
		}
		partText.WriteString(text)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(partText.String(), encodedPixels) {
		t.Fatal("base64 entered ctx_message_part")
	}
	if !strings.Contains(partText.String(), baseline) {
		t.Fatalf("image part lost exact baseline projection: %q", partText.String())
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

func TestAppendTextOnlyWritesNoPartsAndRoundTrips(t *testing.T) {
	db := newLCMTestDB(t)
	defer db.Close()
	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	ctx := context.Background()
	sess := memory.Session{ID: "canonical-text-only", UserID: testUserID, AgentID: "test", Channel: "test"}
	if err := p.Append(ctx, sess,
		ai.UserMessage{Content: "plain user"},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.ToolCall{ID: "plain-call", Name: "echo"}}},
		ai.ToolResultMessage{ToolCallID: "plain-call", ToolName: "echo", Content: []ai.ContentBlock{ai.TextContent{Text: "plain tool"}}},
	); err != nil {
		t.Fatalf("Append: %v", err)
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

	if err := p.Append(ctx, sess, ai.UserMessage{Content: ""}); err != nil {
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

func TestAppendEmptyToolErrorRoundTrips(t *testing.T) {
	db := newLCMTestDB(t)
	defer db.Close()
	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	ctx := context.Background()
	sess := memory.Session{ID: "canonical-empty-tool-error", UserID: testUserID, AgentID: "test", Channel: "test"}
	if err := p.Append(ctx, sess, ai.ToolResultMessage{ToolCallID: "empty-error", ToolName: "bash", IsError: true}); err != nil {
		t.Fatalf("Append: %v", err)
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

func TestAppendRejectsRawImageWithoutWriting(t *testing.T) {
	db := newLCMTestDB(t)
	defer db.Close()
	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	sess := memory.Session{ID: "canonical-reject", UserID: testUserID, AgentID: "test", Channel: "test"}
	err = p.Append(context.Background(), sess, ai.UserMessage{Content: []ai.ContentBlock{
		ai.ImageContent{Data: encodedPixels, MimeType: "image/png"},
	}})
	if !errors.Is(err, ai.ErrRawImageContent) {
		t.Fatalf("Append error = %v, want raw-image rejection", err)
	}
	var count int
	if err := db.QueryRow(context.Background(), `SELECT COUNT(*) FROM ctx_message`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("raw image append wrote %d messages", count)
	}
}

func TestAppendRejectsForeignMedia(t *testing.T) {
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
	err = p.Append(ctx, sess, ai.UserMessage{Content: []ai.ContentBlock{canonicalTestRef(foreignMedia, testBaseline("foreign"))}})
	if err == nil || err.Error() != "canonical media unavailable" {
		t.Fatalf("Append error = %v, want opaque media failure", err)
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM ctx_message`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rejected media wrote %d messages", count)
	}
}

func TestAppendInvalidMediaRollsBackMultipleRows(t *testing.T) {
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
	err = p.Append(ctx, sess,
		ai.UserMessage{Content: []ai.ContentBlock{canonicalTestRef(validMedia, testBaseline("first row"))}},
		ai.UserMessage{Content: []ai.ContentBlock{canonicalTestRef(foreignMedia, testBaseline("second row"))}},
	)
	if err == nil || err.Error() != "canonical media unavailable" {
		t.Fatalf("Append error = %v, want opaque media failure", err)
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

func TestAppendRejectsAssistantImages(t *testing.T) {
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
		{name: "reference", block: canonicalTestRef(mediaID, testBaseline("assistant ref")), want: ai.ErrUnsupportedCanonicalBlock},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := p.Append(context.Background(), sess, ai.AssistantMessage{Content: []ai.ContentBlock{tc.block}})
			if !errors.Is(err, tc.want) {
				t.Fatalf("Append error = %v, want %v", err, tc.want)
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
	if err := p.Append(ctx, sess,
		ai.UserMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "user text"}, canonicalTestRef(userMedia, testBaseline("USER_BASELINE_ONLY"))}},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.ToolCall{ID: "call", Name: "read"}}},
		ai.ToolResultMessage{ToolCallID: "call", ToolName: "read", Content: []ai.ContentBlock{ai.TextContent{Text: "tool text"}, canonicalTestRef(toolMedia, testBaseline("TOOL_BASELINE_ONLY"))}},
	); err != nil {
		t.Fatalf("append canonical image turn: %v", err)
	}
	for i := range 8 {
		if err := p.Append(ctx, sess, ai.UserMessage{Content: "filler"}); err != nil {
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

func canonicalTestRef(mediaID, baseline string) ai.ImageRefContent {
	return ai.ImageRefContent{
		MediaID:  mediaID,
		Baseline: ai.ImageBaseline{Text: baseline},
	}
}

func insertCanonicalTestMedia(t *testing.T, db *pgxpool.Pool, userID, mimeType string) string {
	t.Helper()
	id := uuid.NewString()
	hash := make([]byte, 32)
	copy(hash, id)
	if _, err := db.Exec(context.Background(), `
		INSERT INTO ctx_media (id, user_id, sha256, mime_type, size_bytes)
		VALUES ($1, $2, $3, $4, 1)
	`, id, userID, hash, mimeType); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	return id
}
