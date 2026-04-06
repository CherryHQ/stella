package memorytest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/memory"
)

// RunConformance runs the standard conformance suite against any Provider.
// It tests: Bootstrap idempotency, Append ordering, Assemble budget/freshTail,
// Stats accuracy, and any optional capabilities detected via type assertion.
func RunConformance(t *testing.T, provider memory.Provider) {
	t.Helper()

	ctx := context.Background()
	session := memory.Session{
		ID:      "test:cli:1:main",
		AgentID: "test",
		UserID:  1,
		Channel: "cli",
	}

	t.Run("Name", func(t *testing.T) {
		name := provider.Name()
		if name == "" {
			t.Error("Name() returned empty string")
		}
	})

	t.Run("Bootstrap_idempotency", func(t *testing.T) {
		if err := provider.Bootstrap(ctx, session); err != nil {
			t.Fatalf("first Bootstrap: %v", err)
		}
		if err := provider.Bootstrap(ctx, session); err != nil {
			t.Fatalf("second Bootstrap: %v", err)
		}
	})

	t.Run("Append_ordering", func(t *testing.T) {
		now := time.Now().UTC()
		msgs := make([]ai.Message, 5)
		for i := range msgs {
			msgs[i] = ai.UserMessage{
				Content:   fmt.Sprintf("message %c", 'A'+i),
				Timestamp: now.Add(time.Duration(i) * time.Second),
			}
		}

		if err := provider.Append(ctx, session, msgs...); err != nil {
			t.Fatalf("Append: %v", err)
		}

		// Assemble with large budget to get everything back.
		got, err := provider.Assemble(ctx, session, 100000, 0)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		if len(got) < len(msgs) {
			t.Fatalf("expected at least %d messages, got %d", len(msgs), len(got))
		}

		// Verify ordering: messages should be in chronological order.
		for i := 1; i < len(got); i++ {
			prevTS := memory.MessageTimestamp(got[i-1])
			curTS := memory.MessageTimestamp(got[i])
			if curTS.Before(prevTS) {
				t.Errorf("messages not in chronological order at index %d", i)
			}
		}
	})

	t.Run("Assemble_freshTail", func(t *testing.T) {
		// Assemble with very small budget but freshTail=3.
		// Should always get at least 3 messages.
		got, err := provider.Assemble(ctx, session, 1, 3)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}
		if len(got) < 3 {
			t.Errorf("freshTail=3 but got %d messages", len(got))
		}
	})

	t.Run("Assemble_budget", func(t *testing.T) {
		// Assemble with tight budget and freshTail=0.
		got, err := provider.Assemble(ctx, session, 10, 0)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}
		// With a budget of ~10 tokens and messages of ~3 tokens each,
		// we should not get all 5 messages back.
		totalTokens := 0
		for _, m := range got {
			totalTokens += memory.EstimateTokens(memory.MessageText(m))
		}
		// Budget of 10 tokens with ~3 token messages should yield < 5 messages.
		if len(got) >= 5 {
			t.Errorf("budget=10 should limit results, got %d messages (%d tokens)", len(got), totalTokens)
		}
	})

	t.Run("Stats_accuracy", func(t *testing.T) {
		stats, err := provider.Stats(ctx, session)
		if err != nil {
			t.Fatalf("Stats: %v", err)
		}
		if stats.MessageCount < 5 {
			t.Errorf("expected at least 5 messages, got %d", stats.MessageCount)
		}
		if stats.TokenCount <= 0 {
			t.Errorf("expected positive token count, got %d", stats.TokenCount)
		}
	})

	t.Run("Stats_nonexistent_session", func(t *testing.T) {
		noSession := memory.Session{ID: "nonexistent:session:0:none", AgentID: "none"}
		stats, err := provider.Stats(ctx, noSession)
		if err != nil {
			t.Fatalf("Stats on nonexistent session should not error, got: %v", err)
		}
		if stats.MessageCount != 0 {
			t.Errorf("expected 0 messages for nonexistent session, got %d", stats.MessageCount)
		}
	})

	// --- Optional capabilities ---

	if c, ok := provider.(memory.Compactor); ok {
		t.Run("Compactor", func(t *testing.T) {
			_ = c.NeedsCompaction(ctx, session, 0.75)

			result, err := c.Compact(ctx, session, memory.CompactionIncremental)
			if err != nil {
				t.Fatalf("Compact: %v", err)
			}
			if result == nil {
				t.Fatal("Compact returned nil result")
			}
		})
	}

	if s, ok := provider.(memory.Searcher); ok {
		t.Run("Searcher", func(t *testing.T) {
			results, err := s.Search(ctx, session, memory.SearchQuery{
				Text:  "message",
				Limit: 10,
			})
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if len(results) == 0 {
				t.Error("Search returned no results for 'message' which should match appended content")
			}
			for _, r := range results {
				if r.SourceType == "" {
					t.Error("SearchResult.SourceType is empty")
				}
				if r.Content == "" {
					t.Error("SearchResult.Content is empty")
				}
			}
		})
	}

	if e, ok := provider.(memory.Explorer); ok {
		t.Run("Explorer", func(t *testing.T) {
			// Explorer requires summaries to exist. We test that the method
			// returns an error for a nonexistent summary (not a panic).
			_, err := e.Describe(ctx, "nonexistent_summary")
			if err == nil {
				t.Error("Describe should error for nonexistent summary")
			}

			_, err = e.Expand(ctx, "nonexistent_summary", 1000)
			if err == nil {
				t.Error("Expand should error for nonexistent summary")
			}
		})
	}

	if ps, ok := provider.(memory.ProfileStore); ok {
		t.Run("ProfileStore", func(t *testing.T) {
			// Get nonexistent profile — should return ("", nil).
			content, err := ps.GetProfile(ctx, 999, "nonexistent")
			if err != nil {
				t.Fatalf("GetProfile for nonexistent: %v", err)
			}
			if content != "" {
				t.Errorf("expected empty profile, got %q", content)
			}

			// Set and get round-trip.
			if err := ps.SetProfile(ctx, 1, "test", "likes Go"); err != nil {
				t.Fatalf("SetProfile: %v", err)
			}
			content, err = ps.GetProfile(ctx, 1, "test")
			if err != nil {
				t.Fatalf("GetProfile: %v", err)
			}
			if content != "likes Go" {
				t.Errorf("expected 'likes Go', got %q", content)
			}

			// Replace semantics.
			if err := ps.SetProfile(ctx, 1, "test", "prefers Rust now"); err != nil {
				t.Fatalf("SetProfile replace: %v", err)
			}
			content, err = ps.GetProfile(ctx, 1, "test")
			if err != nil {
				t.Fatalf("GetProfile after replace: %v", err)
			}
			if content != "prefers Rust now" {
				t.Errorf("expected 'prefers Rust now', got %q", content)
			}
		})

		t.Run("AgentSoul", func(t *testing.T) {
			// Get nonexistent soul — should return ("", nil).
			soul, err := ps.GetAgentSoul(ctx, 999, "nonexistent")
			if err != nil {
				t.Fatalf("GetAgentSoul for nonexistent: %v", err)
			}
			if soul != "" {
				t.Errorf("expected empty soul, got %q", soul)
			}

			// Set and get round-trip.
			if err := ps.SetAgentSoul(ctx, 1, "test", "You are friendly and concise."); err != nil {
				t.Fatalf("SetAgentSoul: %v", err)
			}
			soul, err = ps.GetAgentSoul(ctx, 1, "test")
			if err != nil {
				t.Fatalf("GetAgentSoul: %v", err)
			}
			if soul != "You are friendly and concise." {
				t.Errorf("expected soul text, got %q", soul)
			}

			// Replace semantics.
			if err := ps.SetAgentSoul(ctx, 1, "test", "Be formal."); err != nil {
				t.Fatalf("SetAgentSoul replace: %v", err)
			}
			soul, err = ps.GetAgentSoul(ctx, 1, "test")
			if err != nil {
				t.Fatalf("GetAgentSoul after replace: %v", err)
			}
			if soul != "Be formal." {
				t.Errorf("expected 'Be formal.', got %q", soul)
			}

			// Soul and profile are independent.
			profile, err := ps.GetProfile(ctx, 1, "test")
			if err != nil {
				t.Fatalf("GetProfile after soul update: %v", err)
			}
			if profile != "prefers Rust now" {
				t.Errorf("soul update should not affect profile, got %q", profile)
			}
		})
	}

	if sm, ok := provider.(memory.SessionManager); ok {
		t.Run("SessionManager", func(t *testing.T) {
			now := time.Now().UTC()
			info := memory.SessionInfo{
				ID:         session.ID,
				AgentID:    session.AgentID,
				UserID:     session.UserID,
				Channel:    session.Channel,
				Title:      "Test Session",
				CreatedAt:  now,
				LastActive: now,
			}

			if err := sm.SaveInfo(ctx, info); err != nil {
				t.Fatalf("SaveInfo: %v", err)
			}

			loaded, err := sm.LoadInfo(ctx, session.ID)
			if err != nil {
				t.Fatalf("LoadInfo: %v", err)
			}
			if loaded.Title != "Test Session" {
				t.Errorf("expected title 'Test Session', got %q", loaded.Title)
			}

			listed, err := sm.ListInfo(ctx, memory.ListOptions{AgentID: "test"})
			if err != nil {
				t.Fatalf("ListInfo: %v", err)
			}
			if len(listed) == 0 {
				t.Error("ListInfo returned no results for agent 'test'")
			}

			history, err := sm.LoadHistory(ctx, session.ID)
			if err != nil {
				t.Fatalf("LoadHistory: %v", err)
			}
			if len(history) < 5 {
				t.Errorf("expected at least 5 messages in history, got %d", len(history))
			}
		})
	}

	if rs, ok := provider.(memory.ReviewSource); ok {
		t.Run("ReviewSource", func(t *testing.T) {
			// BuildReviewContext with zero since — should return all content.
			text, err := rs.BuildReviewContext(ctx, session, time.Time{})
			if err != nil {
				t.Fatalf("BuildReviewContext: %v", err)
			}
			if text == "" {
				t.Error("BuildReviewContext returned empty for a session with messages")
			}

			// MarkReviewed with a timestamp well after all test messages.
			watermark := time.Now().UTC().Add(time.Minute)
			if err := rs.MarkReviewed(ctx, session, watermark); err != nil {
				t.Fatalf("MarkReviewed: %v", err)
			}

			// ListUnreviewed should not include this session now
			// (unless new messages were added after the watermark).
			candidates, err := rs.ListUnreviewed(ctx, "test", 10)
			if err != nil {
				t.Fatalf("ListUnreviewed: %v", err)
			}
			for _, c := range candidates {
				if c.Session.ID == session.ID {
					t.Error("session should not appear in ListUnreviewed after MarkReviewed")
				}
			}
		})
	}
}
