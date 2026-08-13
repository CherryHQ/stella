package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
)

// rotatingMemory is a session store whose row can be archived by something else
// while a turn is running — a `/new` from another surface. It records what
// the turn tried to write back so a test
// can prove the turn never resurrects a session the chat has left.
type rotatingMemory struct {
	*recordingMemory
	mu sync.Mutex
	// archiveDuringCompaction models the widest real window: auto-compaction can
	// run for minutes between the turn resolving its session and writing back its
	// last-active timestamp.
	archiveDuringCompaction bool
	info                    memory.SessionInfo
	saves                   []memory.SessionInfo
}

func newRotatingMemory(info memory.SessionInfo) *rotatingMemory {
	return &rotatingMemory{recordingMemory: &recordingMemory{}, info: info}
}

func (m *rotatingMemory) archive() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.info.Archived = true
}

func (m *rotatingMemory) archived() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.info.Archived
}

func (m *rotatingMemory) saveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.saves)
}

func (m *rotatingMemory) SaveInfo(_ context.Context, info memory.SessionInfo) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saves = append(m.saves, info)
	// The production UPDATE writes `archived` verbatim; mirror that so the test
	// fails loudly if the turn ever reaches this path with a stale false.
	m.info = info
	return nil
}

func (m *rotatingMemory) ArchiveInfo(_ context.Context, _ memory.SessionInfo) (bool, error) {
	m.archive()
	return true, nil
}

// TouchActiveInfo mirrors the guarded UPDATE: matching an active row and writing
// it are one step, so nothing an archive committed in between can be replayed.
func (m *rotatingMemory) TouchActiveInfo(_ context.Context, info memory.SessionInfo) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.info.Archived {
		return false, nil
	}
	m.saves = append(m.saves, info)
	if m.info.Title == "" {
		m.info.Title = info.Title
	}
	m.info.LastActive = info.LastActive
	return true, nil
}

func (m *rotatingMemory) LoadInfo(_ context.Context, _ string) (memory.SessionInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.info, nil
}

func (m *rotatingMemory) RotateInfo(context.Context, string, memory.SessionInfo) error { return nil }

func (m *rotatingMemory) ListInfo(context.Context, memory.ListOptions) ([]memory.SessionInfo, error) {
	return nil, nil
}

func (m *rotatingMemory) LoadHistory(context.Context, string) ([]ai.Message, error) { return nil, nil }

func (m *rotatingMemory) NeedsCompaction(context.Context, memory.Session, float64) bool {
	return m.archiveDuringCompaction
}

func (m *rotatingMemory) Compact(context.Context, memory.Session, memory.CompactionMode) (*memory.CompactionResult, error) {
	m.archive()
	return &memory.CompactionResult{}, nil
}

func runOneTurn(t *testing.T, rt *Runtime, info session.Info) {
	t.Helper()
	out := rt.Chat(context.Background(), info, "hello")
	for evt := range out {
		if evt.Err != nil {
			t.Fatalf("chat: %v", evt.Err)
		}
	}
}

// TestChatDoesNotResurrectSessionArchivedMidTurn pins the rule that makes
// mid-turn rotation safe: a turn writes back its last-active timestamp from the
// snapshot it resolved, and that snapshot says the session is active. If a
// rotation archives the session first, replaying the snapshot would un-archive
// it — and a kind=chat row has no unique-active index to catch that, so the
// resurrected session would win its binding's newest-match lookup and drag the
// chat back into a conversation the user already left.
func TestChatDoesNotResurrectSessionArchivedMidTurn(t *testing.T) {
	info := session.Info{ID: "sess-rotate", UserID: "user-1", AgentID: "agent-1", Kind: string(session.KindChat)}
	rec, err := info.Record()
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	mem := newRotatingMemory(rec)
	mem.archiveDuringCompaction = true

	rt, err := New(Config{
		Memory:     mem,
		Compaction: CompactionConfig{MaxTokens: 1000, KeepTail: 4},
		NewRunner: func(context.Context, RunnerParams) (Runner, error) {
			return chatFakeRunner{events: []Event{{Text: "ok"}}}, nil
		},
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}

	runOneTurn(t, rt, info)

	if !mem.archived() {
		t.Fatal("a session archived mid-turn must stay archived after the turn completes")
	}
	if mem.saveCount() != 0 {
		t.Fatalf("the turn saved session info %d time(s) over an archived session", mem.saveCount())
	}
}

func TestGuestChatRunsAutoCompaction(t *testing.T) {
	const guestID = "22222222-2222-4222-8222-222222222222"
	info := session.Info{ID: "sess-guest", UserID: guestID, GuestID: guestID, AgentID: "agent-1", Kind: string(session.KindChat)}
	rec, err := info.Record()
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	mem := newRotatingMemory(rec)
	mem.archiveDuringCompaction = true
	rt, err := New(Config{
		Memory: mem, Compaction: CompactionConfig{MaxTokens: 1000, KeepTail: 4},
		NewRunner: func(context.Context, RunnerParams) (Runner, error) {
			return chatFakeRunner{events: []Event{{Text: "ok"}}}, nil
		},
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}

	runOneTurn(t, rt, info)
	if !mem.archived() {
		t.Fatal("guest auto-compaction was not invoked")
	}
}

// TestChatSavesInfoForActiveSession is the other half of the contract: the
// archived check must not cost an ordinary turn its title and last-active
// timestamp.
func TestChatSavesInfoForActiveSession(t *testing.T) {
	info := session.Info{ID: "sess-active", UserID: "user-1", AgentID: "agent-1", Kind: string(session.KindChat)}
	rec, err := info.Record()
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	mem := newRotatingMemory(rec)

	rt, err := New(Config{
		Memory: mem,
		NewRunner: func(context.Context, RunnerParams) (Runner, error) {
			return chatFakeRunner{events: []Event{{Text: "ok"}}}, nil
		},
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}

	before := time.Now().Add(-time.Second)
	runOneTurn(t, rt, info)

	if mem.saveCount() != 1 {
		t.Fatalf("session info saves = %d, want 1", mem.saveCount())
	}
	saved := mem.saves[0]
	if saved.Archived {
		t.Fatal("an active session must not be saved as archived")
	}
	if !saved.LastActive.After(before) {
		t.Fatalf("last-active = %v, want a fresh timestamp", saved.LastActive)
	}
	if saved.Title == "" {
		t.Fatal("the first turn should title the session")
	}
}
