package channel

import (
	"context"
	"fmt"
	"testing"
)

type mockPool struct {
	sessions   map[string]SessionInfo
	nextID     int
	compactErr error
}

func newMockPool() *mockPool {
	return &mockPool{sessions: make(map[string]SessionInfo)}
}

func (p *mockPool) ResolveSession(ch string) (SessionInfo, error) {
	if info, ok := p.sessions[ch]; ok {
		return info, nil
	}
	return p.createSession(ch), nil
}

func (p *mockPool) RotateSession(ch string) (SessionInfo, error) {
	return p.createSession(ch), nil
}

func (p *mockPool) createSession(ch string) SessionInfo {
	p.nextID++
	info := SessionInfo{ID: fmt.Sprintf("session-%d", p.nextID)}
	p.sessions[ch] = info
	return info
}

func (p *mockPool) CompactSession(_ context.Context, _ string) (string, error) {
	if p.compactErr != nil {
		return "", p.compactErr
	}
	return "compacted", nil
}

func TestCommanderNew(t *testing.T) {
	pool := newMockPool()
	cmd := NewCommander(pool, nil, nil)

	id, err := cmd.New("test-channel")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty session ID")
	}
}

func TestCommanderCompact(t *testing.T) {
	pool := newMockPool()
	cmd := NewCommander(pool, nil, nil)

	summary, err := cmd.Compact(context.Background(), "test-channel")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if summary != "compacted" {
		t.Errorf("summary = %q, want %q", summary, "compacted")
	}
}

func TestCommanderCompactError(t *testing.T) {
	pool := newMockPool()
	pool.compactErr = fmt.Errorf("db error")
	cmd := NewCommander(pool, nil, nil)

	_, err := cmd.Compact(context.Background(), "test-channel")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCommanderModelSwitch(t *testing.T) {
	pool := newMockPool()
	models := []ModelOption{
		{Provider: "openai", Model: "gpt-4"},
		{Provider: "anthropic", Model: "claude-3"},
	}
	var switched ModelOption
	listFn := func() []ModelOption { return models }
	switchFn := func(p, m string) error { switched = ModelOption{Provider: p, Model: m}; return nil }

	cmd := NewCommander(pool, listFn, switchFn)

	selected, err := cmd.ModelSwitch("ch", 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if selected.Model != "claude-3" {
		t.Errorf("selected = %v, want claude-3", selected)
	}
	if switched.Model != "claude-3" {
		t.Errorf("switchFn not called correctly: %v", switched)
	}
}

func TestCommanderModelSwitchOutOfRange(t *testing.T) {
	pool := newMockPool()
	models := []ModelOption{{Provider: "openai", Model: "gpt-4"}}
	cmd := NewCommander(pool, func() []ModelOption { return models }, nil)

	_, err := cmd.ModelSwitch("ch", 5)
	if err == nil {
		t.Fatal("expected error for out of range index")
	}
}

func TestParseModelArgs(t *testing.T) {
	tests := []struct {
		input     string
		wantIdx   int
		wantQuery string
	}{
		{"", 0, ""},
		{"3", 3, ""},
		{"claude", 0, "claude"},
		{" 2 ", 2, ""},
		{" gpt ", 0, "gpt"},
	}
	for _, tt := range tests {
		idx, query := ParseModelArgs(tt.input)
		if idx != tt.wantIdx || query != tt.wantQuery {
			t.Errorf("ParseModelArgs(%q) = (%d, %q), want (%d, %q)", tt.input, idx, query, tt.wantIdx, tt.wantQuery)
		}
	}
}

func TestIndexModels(t *testing.T) {
	models := []ModelOption{
		{Provider: "a", Model: "1"},
		{Provider: "b", Model: "2"},
	}
	indexed := IndexModels(models)
	if len(indexed) != 2 {
		t.Fatalf("len = %d, want 2", len(indexed))
	}
	if indexed[0].GlobalIdx != 1 || indexed[1].GlobalIdx != 2 {
		t.Errorf("indices = %d, %d; want 1, 2", indexed[0].GlobalIdx, indexed[1].GlobalIdx)
	}
}

func TestFilterModels(t *testing.T) {
	models := []ModelOption{
		{Provider: "openai", Model: "gpt-4"},
		{Provider: "anthropic", Model: "claude-3"},
		{Provider: "openai", Model: "gpt-3.5"},
	}
	filtered := FilterModels(models, "gpt")
	if len(filtered) != 2 {
		t.Fatalf("len = %d, want 2", len(filtered))
	}
	if filtered[0].GlobalIdx != 1 || filtered[1].GlobalIdx != 3 {
		t.Errorf("indices = %d, %d; want 1, 3", filtered[0].GlobalIdx, filtered[1].GlobalIdx)
	}
}
