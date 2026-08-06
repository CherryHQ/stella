package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
)

type sessionImagesFunc func(context.Context, string, string, []ai.ContentBlock) ([]ai.ContentBlock, error)

func (f sessionImagesFunc) Enrich(ctx context.Context, userID, agentID string, blocks []ai.ContentBlock) ([]ai.ContentBlock, error) {
	return f(ctx, userID, agentID, blocks)
}

type recordingMemory struct {
	mu            sync.Mutex
	messages      []ai.Message
	commits       []int64
	appendError   error
	assembleError error
}

type blockingCommitMemory struct {
	*recordingMemory
	started chan struct{}
	release chan struct{}
}

type snapshotRecordingMemory struct {
	*recordingMemory
	snapshot memory.SessionSnapshot
}

type saveInfoContextMemory struct {
	*recordingMemory
	savedAuthzUserID string
}

type groupMemoryWithoutCommitter struct {
	memory.Provider
	ingestor memory.GroupEventIngestor
}

func (m *groupMemoryWithoutCommitter) SyncGroupEventsBefore(ctx context.Context, session memory.Session, triggerSeq int64) error {
	return m.ingestor.SyncGroupEventsBefore(ctx, session, triggerSeq)
}

func (m *groupMemoryWithoutCommitter) AppendGroupTurn(
	ctx context.Context,
	session memory.Session,
	groupMessageID string,
	trigger ai.Message,
	continuation ...ai.Message,
) error {
	return m.ingestor.AppendGroupTurn(ctx, session, groupMessageID, trigger, continuation...)
}

func (m *snapshotRecordingMemory) GetOrCreateSessionSnapshot(context.Context, string, string, string) (memory.SessionSnapshot, error) {
	return m.snapshot, nil
}

func (m *saveInfoContextMemory) SaveInfo(ctx context.Context, _ memory.SessionInfo) error {
	m.savedAuthzUserID = authz.UserIDFromContext(ctx)
	return nil
}

func (*saveInfoContextMemory) LoadInfo(context.Context, string) (memory.SessionInfo, error) {
	return memory.SessionInfo{}, nil
}

func (*saveInfoContextMemory) ListInfo(context.Context, memory.ListOptions) ([]memory.SessionInfo, error) {
	return nil, nil
}

func (*saveInfoContextMemory) LoadHistory(context.Context, string) ([]ai.Message, error) {
	return nil, nil
}

func (*snapshotRecordingMemory) AdvanceSessionSnapshot(context.Context, string, string, string) error {
	return nil
}

func (m *recordingMemory) Name() string { return "recording" }

func (m *recordingMemory) Bootstrap(context.Context, memory.Session) error { return nil }

func (m *recordingMemory) Append(_ context.Context, _ memory.Session, msgs ...ai.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.appendError != nil {
		return m.appendError
	}
	m.messages = append(m.messages, msgs...)
	return nil
}

func (m *recordingMemory) Assemble(context.Context, memory.Session, int, int) ([]ai.Message, error) {
	return nil, m.assembleError
}

func (m *recordingMemory) Stats(context.Context, memory.Session) (memory.SessionStats, error) {
	return memory.SessionStats{}, nil
}

func (m *recordingMemory) Close() error { return nil }

func (m *recordingMemory) CommitGroupCursor(_ context.Context, _ memory.Session, seq int64) error {
	m.mu.Lock()
	m.commits = append(m.commits, seq)
	m.mu.Unlock()
	return nil
}

func (m *blockingCommitMemory) CommitGroupCursor(ctx context.Context, session memory.Session, seq int64) error {
	close(m.started)
	select {
	case <-m.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return m.recordingMemory.CommitGroupCursor(ctx, session, seq)
}

func (*recordingMemory) SyncGroupEventsBefore(context.Context, memory.Session, int64) error {
	return nil
}

func (m *recordingMemory) AppendGroupTurn(
	ctx context.Context,
	session memory.Session,
	_ string,
	trigger ai.Message,
	continuation ...ai.Message,
) error {
	msgs := make([]ai.Message, 0, len(continuation)+1)
	msgs = append(msgs, trigger)
	msgs = append(msgs, continuation...)
	return m.Append(ctx, session, msgs...)
}

type chatFakeRunner struct {
	events   []Event
	system   string
	messages *[]MessageContent
}

type recordingPreAgentHook struct {
	userID string
}

func (*recordingPreAgentHook) Name() string  { return "recording-pre-agent" }
func (*recordingPreAgentHook) Priority() int { return 0 }
func (h *recordingPreAgentHook) OnPreAgentCall(_ context.Context, hctx *hooks.PreAgentCallContext) {
	h.userID = hctx.UserID
}

func (r chatFakeRunner) Chat(_ context.Context, _ []ai.Message, msg MessageContent) <-chan Event {
	if r.messages != nil {
		*r.messages = append(*r.messages, msg)
	}
	ch := make(chan Event, len(r.events))
	for _, evt := range r.events {
		ch <- evt
	}
	close(ch)
	return ch
}

func (r chatFakeRunner) Alive() bool             { return true }
func (r chatFakeRunner) Busy() bool              { return false }
func (r chatFakeRunner) LastActivity() time.Time { return time.Now() }
func (r chatFakeRunner) SystemPrompt() string    { return r.system }
func (r chatFakeRunner) Close() error            { return nil }

func TestRuntimeChatDoesNotExposeGroupOwnerAsHookUser(t *testing.T) {
	mem := &recordingMemory{}
	hook := &recordingPreAgentHook{}
	rt, err := New(Config{
		Memory: mem,
		NewRunner: func(context.Context, RunnerParams) (Runner, error) {
			return chatFakeRunner{events: []Event{{Text: "ok"}}}, nil
		},
		HooksFn: func() []hooks.HookPlugin { return []hooks.HookPlugin{hook} },
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}

	ctx := memory.WithGroupMessageID(context.Background(), "group-message-1")
	ctx = memory.WithGroupSeq(ctx, 1)
	out := rt.Chat(ctx, session.Info{
		ID:      "session-1",
		UserID:  "11111111-1111-4111-8111-111111111111",
		GroupID: "11111111-1111-4111-8111-111111111111",
		AgentID: "agent-1",
	}, "hello")
	for evt := range out {
		if evt.Err != nil {
			t.Fatalf("chat: %v", evt.Err)
		}
	}
	if hook.userID != "" {
		t.Fatalf("group hook UserID = %q, want empty", hook.userID)
	}
}

func TestRuntimeChatDoesNotPutGroupOwnerInSaveInfoAuthzContext(t *testing.T) {
	mem := &saveInfoContextMemory{recordingMemory: &recordingMemory{}}
	rt, err := New(Config{
		Memory: mem,
		NewRunner: func(context.Context, RunnerParams) (Runner, error) {
			return chatFakeRunner{events: []Event{{Text: "ok"}}}, nil
		},
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}

	ctx := memory.WithGroupMessageID(context.Background(), "group-message-1")
	ctx = memory.WithGroupSeq(ctx, 1)
	out := rt.Chat(ctx, session.Info{
		ID:      "session-1",
		UserID:  "11111111-1111-4111-8111-111111111111",
		GroupID: "11111111-1111-4111-8111-111111111111",
		AgentID: "agent-1",
	}, "hello")
	for evt := range out {
		if evt.Err != nil {
			t.Fatalf("chat: %v", evt.Err)
		}
	}
	if mem.savedAuthzUserID != "" {
		t.Fatalf("SaveInfo authz UserID = %q, want empty", mem.savedAuthzUserID)
	}
}

func TestChatRebuildsSnapshotPromptAtVersionZero(t *testing.T) {
	mem := &snapshotRecordingMemory{
		recordingMemory: &recordingMemory{},
		snapshot:        memory.SessionSnapshot{Version: 0},
	}
	var promptCalls int
	var beforeRunSystem string
	rt, err := New(Config{
		Memory: mem,
		NewRunner: func(context.Context, RunnerParams) (Runner, error) {
			return chatFakeRunner{system: "live base prompt", events: []Event{{Text: "ok"}}}, nil
		},
		SnapshotPrompt: func(_ context.Context, _ session.Info, snap memory.SessionSnapshot) string {
			promptCalls++
			if snap.Version != 0 {
				t.Fatalf("snapshot version = %d, want 0", snap.Version)
			}
			return "frozen snapshot prompt"
		},
		BeforeRun: func(_ context.Context, _ session.Info, _, _, system string, _ []ai.Message) (string, error) {
			beforeRunSystem = system
			return "", nil
		},
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}

	out := rt.Chat(context.Background(), session.Info{ID: "sess-1", UserID: "user-1", AgentID: "agent-1"}, "hello")
	for evt := range out {
		if evt.Err != nil {
			t.Fatalf("chat: %v", evt.Err)
		}
	}

	if promptCalls != 1 {
		t.Fatalf("snapshot prompt calls = %d, want 1", promptCalls)
	}
	if beforeRunSystem != "frozen snapshot prompt" {
		t.Fatalf("before-run system = %q, want frozen snapshot prompt", beforeRunSystem)
	}
}

func TestRuntimeChatEnrichesAndCanonicallyAppendsOrdinaryImages(t *testing.T) {
	mem := &recordingMemory{}
	var received []MessageContent
	ref := ai.ImageRefContent{MediaID: "media-1"}
	rt, err := New(Config{
		Memory: mem,
		SessionImages: sessionImagesFunc(func(_ context.Context, userID, agentID string, blocks []ai.ContentBlock) ([]ai.ContentBlock, error) {
			if userID != "user-1" || agentID != "agent-1" || !ai.HasImage(blocks) {
				t.Fatalf("unexpected enrich input: user=%q agent=%q blocks=%#v", userID, agentID, blocks)
			}
			return []ai.ContentBlock{ref}, nil
		}),
		NewRunner: func(context.Context, RunnerParams) (Runner, error) {
			return chatFakeRunner{messages: &received}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := rt.Chat(context.Background(), session.Info{ID: "sess-1", UserID: "user-1", AgentID: "agent-1"}, []ai.ContentBlock{ai.ImageContent{Data: "raw", MimeType: "image/png"}})
	for evt := range out {
		if evt.Err != nil {
			t.Fatalf("chat: %v", evt.Err)
		}
	}
	receivedCanonical := false
	if len(received) == 1 {
		if receivedMsg, ok := received[0].(ai.UserMessage); ok {
			receivedCanonical = containsRuntimeRef(receivedMsg)
		}
	}
	if len(mem.messages) != 1 || !containsRuntimeRef(mem.messages[0]) || !receivedCanonical {
		t.Fatalf("canonical persistence = messages:%#v received:%#v", mem.messages, received)
	}
}

func TestRuntimeChatEnrichesSingularImageBeforeCanonicalAppend(t *testing.T) {
	mem := &recordingMemory{}
	ref := ai.ImageRefContent{MediaID: "media-1"}
	enrichCalls := 0
	rt, err := New(Config{
		Memory: mem,
		SessionImages: sessionImagesFunc(func(_ context.Context, _, _ string, blocks []ai.ContentBlock) ([]ai.ContentBlock, error) {
			enrichCalls++
			if len(blocks) != 1 {
				t.Fatalf("singular image blocks = %#v", blocks)
			}
			if _, ok := blocks[0].(ai.ImageContent); !ok {
				t.Fatalf("enricher received %T, want raw ImageContent", blocks[0])
			}
			return []ai.ContentBlock{ref}, nil
		}),
		NewRunner: func(context.Context, RunnerParams) (Runner, error) { return chatFakeRunner{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	for evt := range rt.Chat(context.Background(), session.Info{ID: "sess-1", UserID: "user-1", AgentID: "agent-1"}, ai.ImageContent{Data: "raw", MimeType: "image/png"}) {
		if evt.Err != nil {
			t.Fatalf("chat: %v", evt.Err)
		}
	}
	if enrichCalls != 1 || len(mem.messages) != 1 || !containsRuntimeRef(mem.messages[0]) {
		t.Fatalf("singular image bypassed canonical pipeline: enrich=%d messages=%#v", enrichCalls, mem.messages)
	}
}

func TestRuntimeChatPassesCanonicalImageRefWithoutEnrichment(t *testing.T) {
	mem := &recordingMemory{}
	ref := ai.ImageRefContent{MediaID: "media-1"}
	rt, err := New(Config{
		Memory: mem,
		SessionImages: sessionImagesFunc(func(context.Context, string, string, []ai.ContentBlock) ([]ai.ContentBlock, error) {
			t.Fatal("canonical ImageRef input must not be re-enriched")
			return nil, nil
		}),
		NewRunner: func(context.Context, RunnerParams) (Runner, error) { return chatFakeRunner{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	for evt := range rt.Chat(context.Background(), session.Info{ID: "sess-1", UserID: "user-1", AgentID: "agent-1"}, ref) {
		if evt.Err != nil {
			t.Fatalf("chat: %v", evt.Err)
		}
	}
	if len(mem.messages) != 1 || !containsRuntimeRef(mem.messages[0]) {
		t.Fatalf("canonical ImageRef did not persist safely: %#v", mem.messages)
	}
}

func TestRuntimeGroupImageKeepsLegacyAppend(t *testing.T) {
	mem := &recordingMemory{}
	rt, err := New(Config{
		Memory: mem,
		SessionImages: sessionImagesFunc(func(context.Context, string, string, []ai.ContentBlock) ([]ai.ContentBlock, error) {
			t.Fatal("groups must not invoke ordinary-session enrichment")
			return nil, nil
		}),
		NewRunner: func(context.Context, RunnerParams) (Runner, error) { return chatFakeRunner{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	info := session.Info{ID: "sess-1", UserID: "11111111-1111-4111-8111-111111111111", AgentID: "agent-1", GroupID: "11111111-1111-4111-8111-111111111111"}
	for evt := range rt.Chat(context.Background(), info, []ai.ContentBlock{ai.ImageContent{Data: "raw", MimeType: "image/png"}}) {
		if evt.Err != nil {
			t.Fatalf("chat: %v", evt.Err)
		}
	}
	if len(mem.messages) != 1 || !ai.HasImage(runtimeTestMessageBlocks(mem.messages[0])) {
		t.Fatalf("group path changed: messages=%#v", mem.messages)
	}
}

func TestRuntimeChatFailsClosedWhenCanonicalImageAppendFails(t *testing.T) {
	mem := &recordingMemory{appendError: errors.New("write failed")}
	rt, err := New(Config{
		Memory: mem,
		SessionImages: sessionImagesFunc(func(context.Context, string, string, []ai.ContentBlock) ([]ai.ContentBlock, error) {
			return []ai.ContentBlock{ai.ImageRefContent{MediaID: "media-1"}}, nil
		}),
		NewRunner: func(context.Context, RunnerParams) (Runner, error) { return chatFakeRunner{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	out := rt.Chat(context.Background(), session.Info{ID: "sess-1", UserID: "user-1", AgentID: "agent-1"}, []ai.ContentBlock{ai.ImageContent{Data: "raw", MimeType: "image/png"}})
	var got error
	for evt := range out {
		if evt.Err != nil {
			got = evt.Err
		}
	}
	if got == nil || len(mem.messages) != 0 {
		t.Fatalf("expected closed image-ref write, err=%v messages=%#v", got, mem.messages)
	}
}

func containsRuntimeRef(msg ai.Message) bool {
	for _, block := range runtimeTestMessageBlocks(msg) {
		if _, ok := block.(ai.ImageRefContent); ok {
			return true
		}
	}
	return false
}

func runtimeTestMessageBlocks(msg ai.Message) []ai.ContentBlock {
	switch msg := msg.(type) {
	case ai.UserMessage:
		blocks, _ := msg.Content.([]ai.ContentBlock)
		return blocks
	case ai.AssistantMessage:
		return msg.Content
	case ai.ToolResultMessage:
		return msg.Content
	default:
		return nil
	}
}

func TestRuntimeChatCommitsGroupCursorAfterSuccessfulGroupTurn(t *testing.T) {
	mem := &recordingMemory{}
	rt, err := New(Config{
		Memory: mem,
		NewRunner: func(context.Context, RunnerParams) (Runner, error) {
			return chatFakeRunner{events: []Event{{Text: "ok"}}}, nil
		},
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	ctx := memory.WithGroupSeq(context.Background(), 42)
	out := make(chan Event, 10)
	rt.chat(ctx, out, session.Info{ID: "sess-1", UserID: "11111111-1111-4111-8111-111111111111", AgentID: "agent-1", GroupID: "11111111-1111-4111-8111-111111111111"}, "hello", chatOptions{})
	for range out {
	}
	if len(mem.commits) != 1 || mem.commits[0] != 42 {
		t.Fatalf("commits = %v, want [42]", mem.commits)
	}
	if len(mem.messages) != 2 {
		t.Fatalf("messages = %d, want user + assistant", len(mem.messages))
	}
	if got := flattenRuntimeUserMessage(mem.messages[0]); got != "hello" {
		t.Fatalf("persisted user = %q", got)
	}
	if _, ok := mem.messages[1].(ai.AssistantMessage); !ok {
		t.Fatalf("second persisted message = %T, want assistant", mem.messages[1])
	}
}

func TestRuntimeChatRejectsGroupMemoryWithoutCursorCommitter(t *testing.T) {
	inner := &recordingMemory{}
	mem := &groupMemoryWithoutCommitter{Provider: inner, ingestor: inner}
	var modelMessages []MessageContent
	rt, err := New(Config{
		Memory: mem,
		NewRunner: func(context.Context, RunnerParams) (Runner, error) {
			return chatFakeRunner{
				events:   []Event{{Text: "unexpected"}},
				messages: &modelMessages,
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}

	out := make(chan Event, 10)
	rt.chat(
		memory.WithGroupSeq(context.Background(), 42),
		out,
		session.Info{
			ID:      "sess-1",
			UserID:  "11111111-1111-4111-8111-111111111111",
			AgentID: "agent-1",
			GroupID: "11111111-1111-4111-8111-111111111111",
		},
		"hello",
		chatOptions{},
	)
	var gotErr error
	for evt := range out {
		if evt.Err != nil {
			gotErr = evt.Err
		}
	}
	if gotErr == nil || !strings.Contains(gotErr.Error(), "group cursor commits") {
		t.Fatalf("error = %v, want missing group cursor committer", gotErr)
	}
	if len(modelMessages) != 0 {
		t.Fatal("model should not run without a group cursor committer")
	}
}

func TestRuntimeChatDoesNotCommitGroupCursorOnChatError(t *testing.T) {
	mem := &recordingMemory{}
	boom := errors.New("boom")
	rt, err := New(Config{
		Memory: mem,
		NewRunner: func(context.Context, RunnerParams) (Runner, error) {
			return chatFakeRunner{events: []Event{{Err: boom}}}, nil
		},
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	ctx := memory.WithGroupSeq(context.Background(), 42)
	out := make(chan Event, 10)
	rt.chat(ctx, out, session.Info{ID: "sess-1", UserID: "11111111-1111-4111-8111-111111111111", AgentID: "agent-1", GroupID: "11111111-1111-4111-8111-111111111111"}, "hello", chatOptions{})
	for range out {
	}
	if len(mem.commits) != 0 {
		t.Fatalf("commits = %v, want none", mem.commits)
	}
	if len(mem.messages) != 0 {
		t.Fatalf("messages = %d, want none on failed group turn", len(mem.messages))
	}
}

func TestRuntimeChatDoesNotCommitGroupCursorWhenContextCanceled(t *testing.T) {
	mem := &recordingMemory{}
	rt, err := New(Config{
		Memory: mem,
		NewRunner: func(context.Context, RunnerParams) (Runner, error) {
			return chatFakeRunner{events: []Event{{Text: "ok"}}}, nil
		},
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	ctx, cancel := context.WithCancel(memory.WithGroupSeq(context.Background(), 42))
	cancel()
	out := make(chan Event, 10)
	rt.chat(ctx, out, session.Info{ID: "sess-1", UserID: "11111111-1111-4111-8111-111111111111", AgentID: "agent-1", GroupID: "11111111-1111-4111-8111-111111111111"}, "hello", chatOptions{})
	for range out {
	}
	if len(mem.commits) != 0 {
		t.Fatalf("commits = %v, want none", mem.commits)
	}
}

func TestRuntimeChatClosesOnlyAfterGroupCursorCommit(t *testing.T) {
	mem := &blockingCommitMemory{
		recordingMemory: &recordingMemory{},
		started:         make(chan struct{}),
		release:         make(chan struct{}),
	}
	rt, err := New(Config{
		Memory: mem,
		NewRunner: func(context.Context, RunnerParams) (Runner, error) {
			return chatFakeRunner{events: []Event{{Text: "ok"}}}, nil
		},
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	ctx := memory.WithGroupMessageID(context.Background(), "group-message-1")
	ctx = memory.WithGroupSeq(ctx, 42)
	out := rt.Chat(ctx, session.Info{
		ID:      "sess-1",
		UserID:  "11111111-1111-4111-8111-111111111111",
		AgentID: "agent-1",
		GroupID: "11111111-1111-4111-8111-111111111111",
	}, "hello")

	if evt := <-out; evt.Text != "ok" {
		t.Fatalf("first event = %#v, want text event", evt)
	}
	select {
	case <-mem.started:
	case <-time.After(time.Second):
		t.Fatal("group cursor commit did not start")
	}
	select {
	case _, ok := <-out:
		if !ok {
			t.Fatal("chat stream closed before group cursor commit completed")
		}
	case <-time.After(50 * time.Millisecond):
		// The stream stays open while the durable commit is in flight.
	}

	close(mem.release)
	select {
	case _, ok := <-out:
		if ok {
			t.Fatal("unexpected event after group cursor commit")
		}
	case <-time.After(time.Second):
		t.Fatal("chat stream did not close after group cursor commit")
	}
}

func TestRuntimeChatDoesNotPersistGroupPartialOnTimeout(t *testing.T) {
	mem := &recordingMemory{}
	rt, err := New(Config{
		Memory: mem,
		NewRunner: func(context.Context, RunnerParams) (Runner, error) {
			return chatFakeRunner{events: []Event{{Text: "partial"}, {Err: ErrChatTimeout}}}, nil
		},
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	ctx := memory.WithGroupSeq(context.Background(), 42)
	out := make(chan Event, 10)
	rt.chat(ctx, out, session.Info{ID: "sess-1", UserID: "11111111-1111-4111-8111-111111111111", AgentID: "agent-1", GroupID: "11111111-1111-4111-8111-111111111111"}, "hello", chatOptions{})
	for range out {
	}
	if len(mem.commits) != 0 {
		t.Fatalf("commits = %v, want none", mem.commits)
	}
	if len(mem.messages) != 0 {
		t.Fatalf("messages = %d, want none on timeout", len(mem.messages))
	}
}

func TestRuntimeChatDoesNotPersistGroupStoreBeforeLaterError(t *testing.T) {
	mem := &recordingMemory{}
	boom := errors.New("boom")
	rt, err := New(Config{
		Memory: mem,
		NewRunner: func(context.Context, RunnerParams) (Runner, error) {
			return chatFakeRunner{events: []Event{{Store: ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "stored"}}}}, {Err: boom}}}, nil
		},
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	ctx := memory.WithGroupSeq(context.Background(), 42)
	out := make(chan Event, 10)
	rt.chat(ctx, out, session.Info{ID: "sess-1", UserID: "11111111-1111-4111-8111-111111111111", AgentID: "agent-1", GroupID: "11111111-1111-4111-8111-111111111111"}, "hello", chatOptions{})
	for range out {
	}
	if len(mem.commits) != 0 {
		t.Fatalf("commits = %v, want none", mem.commits)
	}
	if len(mem.messages) != 0 {
		t.Fatalf("messages = %d, want none after store then error", len(mem.messages))
	}
}

func TestRuntimeChatDoesNotCommitGroupCursorWhenStoreFails(t *testing.T) {
	mem := &recordingMemory{appendError: errors.New("append failed")}
	rt, err := New(Config{
		Memory: mem,
		NewRunner: func(context.Context, RunnerParams) (Runner, error) {
			return chatFakeRunner{events: []Event{{Text: "ok"}}}, nil
		},
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	ctx := memory.WithGroupSeq(context.Background(), 42)
	out := make(chan Event, 10)
	rt.chat(ctx, out, session.Info{ID: "sess-1", UserID: "11111111-1111-4111-8111-111111111111", AgentID: "agent-1", GroupID: "11111111-1111-4111-8111-111111111111"}, "hello", chatOptions{})
	for range out {
	}
	if len(mem.commits) != 0 {
		t.Fatalf("commits = %v, want none", mem.commits)
	}
}

func TestRuntimeChatDoesNotCommitGroupCursorWhenAssembleFails(t *testing.T) {
	assembleErr := errors.New("assemble failed")
	mem := &recordingMemory{assembleError: assembleErr}
	rt, err := New(Config{
		Memory: mem,
		NewRunner: func(context.Context, RunnerParams) (Runner, error) {
			return chatFakeRunner{events: []Event{{Text: "ok"}}}, nil
		},
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	ctx := memory.WithGroupSeq(context.Background(), 42)
	out := make(chan Event, 10)
	rt.chat(ctx, out, session.Info{ID: "sess-1", UserID: "11111111-1111-4111-8111-111111111111", AgentID: "agent-1", GroupID: "11111111-1111-4111-8111-111111111111"}, "hello", chatOptions{})
	var gotErr bool
	for evt := range out {
		if errors.Is(evt.Err, assembleErr) {
			gotErr = true
		}
	}
	if !gotErr {
		t.Fatal("expected assemble error event")
	}
	if len(mem.commits) != 0 {
		t.Fatalf("commits = %v, want none", mem.commits)
	}
	if len(mem.messages) != 0 {
		t.Fatalf("messages = %d, want none on assemble failure", len(mem.messages))
	}
}

func flattenRuntimeUserMessage(msg ai.Message) string {
	um, ok := msg.(ai.UserMessage)
	if !ok {
		return ""
	}
	switch c := um.Content.(type) {
	case string:
		return c
	case []ai.ContentBlock:
		return ai.FlattenText(c)
	default:
		return fmt.Sprintf("%v", c)
	}
}

func TestStreamEventsDoesNotDuplicateBufferedAssistantStore(t *testing.T) {
	mem := &recordingMemory{}
	rt := &Runtime{mem: mem, log: slog.Default()}

	stream := make(chan Event, 3)
	out := make(chan Event, 3)
	stream <- Event{Reasoning: "thinking"}
	stream <- Event{Text: "answer"}
	stream <- Event{Store: ai.AssistantMessage{Content: []ai.ContentBlock{
		ai.ThinkingContent{Thinking: "thinking"},
		ai.TextContent{Text: "answer"},
		ai.ToolCall{ID: "tool-1", Name: "search", Arguments: map[string]any{"q": "x"}},
	}}}
	close(stream)

	if err := rt.streamEvents(context.Background(), "session-1", memory.Session{ID: "session-1"}, stream, out, hooks.NewHookSet(nil), hooks.HookMeta{}, time.Now(), "", nil); err != nil {
		t.Fatalf("stream events: %v", err)
	}
	close(out)
	for range out {
	}

	if len(mem.messages) != 1 {
		t.Fatalf("expected one persisted assistant message, got %d", len(mem.messages))
	}
	msg, ok := mem.messages[0].(ai.AssistantMessage)
	if !ok {
		t.Fatalf("expected assistant message, got %T", mem.messages[0])
	}
	if len(msg.Content) != 3 {
		t.Fatalf("expected 3 content blocks, got %d", len(msg.Content))
	}
	if got := msg.Content[0].(ai.ThinkingContent).Thinking; got != "thinking" {
		t.Fatalf("thinking = %q", got)
	}
	if got := msg.Content[1].(ai.TextContent).Text; got != "answer" {
		t.Fatalf("text = %q", got)
	}
}

func TestStreamEvents_TimeoutDoesNotForwardError(t *testing.T) {
	mem := &recordingMemory{}
	rt := &Runtime{mem: mem, log: slog.Default()}

	stream := make(chan Event, 3)
	out := make(chan Event, 10)
	stream <- Event{Text: "partial"}
	stream <- Event{Err: ErrChatTimeout}
	close(stream)

	if err := rt.streamEvents(context.Background(), "sess-1", memory.Session{ID: "sess-1"}, stream, out, hooks.NewHookSet(nil), hooks.HookMeta{}, time.Now(), "", nil); !errors.Is(err, ErrChatTimeout) {
		t.Fatalf("stream events error = %v, want timeout", err)
	}
	close(out)

	var events []Event
	for evt := range out {
		events = append(events, evt)
	}

	// Should have: text "partial", then the timeout notice text.
	// Should NOT have an Err event.
	for _, evt := range events {
		if evt.Err != nil {
			t.Fatalf("timeout should not forward error to caller, got: %v", evt.Err)
		}
	}
	if len(events) < 2 {
		t.Fatalf("expected at least 2 events (partial + notice), got %d", len(events))
	}
}

func TestStreamEvents_NonTimeoutErrorForwarded(t *testing.T) {
	mem := &recordingMemory{}
	rt := &Runtime{mem: mem, log: slog.Default()}

	stream := make(chan Event, 2)
	out := make(chan Event, 10)
	realErr := fmt.Errorf("provider error")
	stream <- Event{Err: realErr}
	close(stream)

	if err := rt.streamEvents(context.Background(), "sess-1", memory.Session{ID: "sess-1"}, stream, out, hooks.NewHookSet(nil), hooks.HookMeta{}, time.Now(), "", nil); !errors.Is(err, realErr) {
		t.Fatalf("stream events error = %v, want provider error", err)
	}
	close(out)

	var gotErr bool
	for evt := range out {
		if evt.Err != nil && errors.Is(evt.Err, realErr) {
			gotErr = true
		}
	}
	if !gotErr {
		t.Fatal("non-timeout errors should be forwarded to caller")
	}
}
