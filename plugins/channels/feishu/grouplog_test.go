package feishu

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAppendAndRecentChronological(t *testing.T) {
	gl := NewGroupLog(10)
	now := time.Now()
	gl.Append(GroupEntry{Timestamp: now, SenderID: "u1", Text: "first"})
	gl.Append(GroupEntry{Timestamp: now.Add(time.Second), SenderID: "u2", Text: "second"})
	gl.Append(GroupEntry{Timestamp: now.Add(2 * time.Second), SenderID: "u3", Text: "third"})

	entries := gl.Recent(3)
	if len(entries) != 3 {
		t.Fatalf("len = %d, want 3", len(entries))
	}
	if entries[0].Text != "first" || entries[1].Text != "second" || entries[2].Text != "third" {
		t.Errorf("order wrong: %v", entries)
	}
}

func TestOverflowDropsOldest(t *testing.T) {
	gl := NewGroupLog(3)
	for i := range 5 {
		gl.Append(GroupEntry{SenderID: "u", Text: string(rune('a' + i))})
	}

	entries := gl.Recent(10)
	if len(entries) != 3 {
		t.Fatalf("len = %d, want 3", len(entries))
	}
	if entries[0].Text != "c" || entries[1].Text != "d" || entries[2].Text != "e" {
		t.Errorf("expected [c d e], got [%s %s %s]", entries[0].Text, entries[1].Text, entries[2].Text)
	}
}

func TestRecentMoreThanCount(t *testing.T) {
	gl := NewGroupLog(10)
	gl.Append(GroupEntry{Text: "only"})

	entries := gl.Recent(5)
	if len(entries) != 1 {
		t.Fatalf("len = %d, want 1", len(entries))
	}
	if entries[0].Text != "only" {
		t.Errorf("text = %q", entries[0].Text)
	}
}

func TestRecentZero(t *testing.T) {
	gl := NewGroupLog(10)
	gl.Append(GroupEntry{Text: "x"})

	entries := gl.Recent(0)
	if len(entries) != 0 {
		t.Errorf("len = %d, want 0", len(entries))
	}
}

func TestFormatContextWithNames(t *testing.T) {
	gl := NewGroupLog(10)
	ts := time.Date(2026, 1, 1, 10, 30, 0, 0, time.UTC)
	gl.Append(GroupEntry{Timestamp: ts, SenderID: "u1", Text: "hey can someone help?"})
	gl.Append(GroupEntry{Timestamp: ts.Add(time.Minute), SenderID: "u2", Text: "sure, what's broken?"})

	resolver := func(id string) string {
		switch id {
		case "u1":
			return "Alice"
		case "u2":
			return "Bob"
		}
		return ""
	}

	got := gl.FormatContext(2, resolver)
	if !strings.Contains(got, "[Alice 10:30]: hey can someone help?") {
		t.Errorf("missing Alice line in:\n%s", got)
	}
	if !strings.Contains(got, "[Bob 10:31]: sure, what's broken?") {
		t.Errorf("missing Bob line in:\n%s", got)
	}
}

func TestFormatContextFallbackToEntryName(t *testing.T) {
	gl := NewGroupLog(10)
	ts := time.Date(2026, 1, 1, 14, 0, 0, 0, time.UTC)
	gl.Append(GroupEntry{Timestamp: ts, SenderID: "u1", Name: "Carol", Text: "hi"})

	got := gl.FormatContext(1, func(string) string { return "" })
	if !strings.Contains(got, "[Carol 14:00]: hi") {
		t.Errorf("expected fallback to entry.Name, got:\n%s", got)
	}
}

func TestFormatContextFallbackToSenderID(t *testing.T) {
	gl := NewGroupLog(10)
	ts := time.Date(2026, 1, 1, 14, 0, 0, 0, time.UTC)
	gl.Append(GroupEntry{Timestamp: ts, SenderID: "ou_xyz", Text: "hi"})

	got := gl.FormatContext(1, func(string) string { return "" })
	if !strings.Contains(got, "[ou_xyz 14:00]: hi") {
		t.Errorf("expected fallback to SenderID, got:\n%s", got)
	}
}

func TestFormatContextNilResolver(t *testing.T) {
	gl := NewGroupLog(10)
	ts := time.Date(2026, 1, 1, 14, 0, 0, 0, time.UTC)
	gl.Append(GroupEntry{Timestamp: ts, SenderID: "u1", Name: "Dave", Text: "hello"})

	got := gl.FormatContext(1, nil)
	if !strings.Contains(got, "[Dave 14:00]: hello") {
		t.Errorf("nil resolver should fall back to Name, got:\n%s", got)
	}
}

func TestFormatContextEmpty(t *testing.T) {
	gl := NewGroupLog(10)
	got := gl.FormatContext(5, nil)
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestConcurrentAppendSafety(t *testing.T) {
	gl := NewGroupLog(50)
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			gl.Append(GroupEntry{SenderID: "u", Text: string(rune('a' + n%26))})
		}(i)
	}
	wg.Wait()

	entries := gl.Recent(50)
	if len(entries) != 50 {
		t.Errorf("expected 50 entries after overflow, got %d", len(entries))
	}
}

func TestNewGroupLogDefaultCapacity(t *testing.T) {
	gl := NewGroupLog(0)
	if gl.cap != 100 {
		t.Errorf("cap = %d, want 100", gl.cap)
	}
}

func TestNewGroupLogNegativeCapacity(t *testing.T) {
	gl := NewGroupLog(-5)
	if gl.cap != 100 {
		t.Errorf("cap = %d, want 100", gl.cap)
	}
}

func TestBotGroupLogLazyCreation(t *testing.T) {
	bot := &Bot{}
	gl1 := bot.groupLog("oc_chat1")
	gl2 := bot.groupLog("oc_chat1")
	if gl1 != gl2 {
		t.Error("same chatID should return same GroupLog instance")
	}

	gl3 := bot.groupLog("oc_chat2")
	if gl1 == gl3 {
		t.Error("different chatIDs should return different GroupLog instances")
	}
}
