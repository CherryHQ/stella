package cli

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vaayne/anna/internal/agent"
	"github.com/vaayne/anna/internal/agent/runner"
	appdb "github.com/vaayne/anna/internal/db"
	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/memory"
	lcmmemory "github.com/vaayne/anna/plugins/memory/lcm"
)

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func newTestMemProvider() memory.Provider {
	dir, _ := os.MkdirTemp("", "cli-test-*")
	db, _ := appdb.OpenDB(filepath.Join(dir, "test.db"))
	p, _ := lcmmemory.New(db, nil, nil)
	return p
}

// mockRunner implements runner.Runner for testing.
type mockRunner struct {
	events []runner.Event
}

func (m *mockRunner) Chat(_ context.Context, _ []ai.Message, _ runner.MessageContent) <-chan runner.Event {
	ch := make(chan runner.Event, len(m.events))
	for _, e := range m.events {
		ch <- e
	}
	close(ch)
	return ch
}

func newTestPool(events []runner.Event) *agent.Pool {
	factory := func(_ context.Context, _ runner.RunnerParams) (runner.Runner, error) {
		return &mockRunner{events: events}, nil
	}
	return agent.NewPool(factory, newTestMemProvider())
}

// initModel creates a chatModel and sends an initial WindowSizeMsg so the viewport is ready.
func initModel(t *testing.T, pool *agent.Pool) chatModel {
	t.Helper()
	m := newChatModel(context.Background(), pool, "test", "test-model", nil, nil)
	result, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return result.(chatModel)
}

func TestChatModelQuit(t *testing.T) {
	pool := newTestPool(nil)
	defer func() { _ = pool.Close() }()

	m := initModel(t, pool)

	m.textarea.SetValue("/quit")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if cmd == nil {
		t.Fatal("expected quit command, got nil")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg, got %T", msg)
	}
}

func TestChatModelExit(t *testing.T) {
	pool := newTestPool(nil)
	defer func() { _ = pool.Close() }()

	m := initModel(t, pool)

	m.textarea.SetValue("/exit")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if cmd == nil {
		t.Fatal("expected quit command, got nil")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg, got %T", msg)
	}
}

func TestChatModelNewSession(t *testing.T) {
	pool := newTestPool(nil)
	defer func() { _ = pool.Close() }()

	m := initModel(t, pool)

	m.textarea.SetValue("/new")
	result, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := result.(chatModel)

	if !strings.Contains(updated.history.String(), "new session started") {
		t.Errorf("expected new session message in history, got: %s", updated.history.String())
	}
}

func TestChatModelSkipsEmpty(t *testing.T) {
	pool := newTestPool(nil)
	defer func() { _ = pool.Close() }()

	m := initModel(t, pool)

	m.textarea.SetValue("")
	result, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := result.(chatModel)

	if updated.streaming {
		t.Error("should not be streaming on empty input")
	}
	if updated.history.Len() > 0 {
		t.Error("expected empty history for empty input")
	}
}

func TestChatModelStreaming(t *testing.T) {
	pool := newTestPool(nil)
	defer func() { _ = pool.Close() }()

	m := initModel(t, pool)

	m.textarea.SetValue("hello")
	result, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = result.(chatModel)

	if !m.streaming {
		t.Error("expected streaming to be true after sending prompt")
	}
	if cmd == nil {
		t.Fatal("expected a command to start streaming")
	}
	if !strings.Contains(m.history.String(), "You") {
		t.Error("expected user message in history")
	}

	// Simulate streamStartMsg with a fake channel.
	ch := make(chan runner.Event, 3)
	ch <- runner.Event{Text: "Hello"}
	ch <- runner.Event{Text: " world"}
	close(ch)

	result, cmd = m.Update(streamStartMsg{stream: ch})
	m = result.(chatModel)

	// Drain all chunks.
	for cmd != nil {
		msg := cmd()
		result, cmd = m.Update(msg)
		m = result.(chatModel)
	}

	if m.streaming {
		t.Error("expected streaming to be false after stream done")
	}
	plain := ansiRe.ReplaceAllString(m.history.String(), "")
	if !strings.Contains(plain, "Hello world") {
		t.Errorf("expected 'Hello world' in history, got: %s", m.history.String())
	}
}

func TestChatModelCtrlCQuits(t *testing.T) {
	pool := newTestPool(nil)
	defer func() { _ = pool.Close() }()

	m := initModel(t, pool)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("expected quit command on ctrl+c")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg, got %T", msg)
	}
}

func TestChatModelAbort(t *testing.T) {
	pool := newTestPool(nil)
	defer func() { _ = pool.Close() }()

	m := initModel(t, pool)

	m.textarea.SetValue("/abort")
	result, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := result.(chatModel)

	if !strings.Contains(updated.history.String(), "abort") {
		t.Errorf("expected abort message in history, got: %s", updated.history.String())
	}
}

func TestChatModelHelp(t *testing.T) {
	pool := newTestPool(nil)
	defer func() { _ = pool.Close() }()

	m := initModel(t, pool)

	m.textarea.SetValue("/help")
	result, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := result.(chatModel)

	history := updated.history.String()
	if !strings.Contains(history, "Anna CLI commands") {
		t.Errorf("expected CLI help heading, got: %s", history)
	}
	if !strings.Contains(history, "/new") {
		t.Error("expected /new command in help output")
	}
	if !strings.Contains(history, "Natural-language shortcuts") {
		t.Error("expected CLI help to explain channel-only natural-language shortcuts")
	}
	if !strings.Contains(history, "Ctrl+C") {
		t.Error("expected CLI help to mention Ctrl+C")
	}
}

func TestSlashCommandsIncludesHelpAndExcludesAbort(t *testing.T) {
	cmds := make(map[string]slashCommand)
	for _, cmd := range slashCommands {
		cmds[cmd.name] = cmd
	}

	if _, ok := cmds["/abort"]; ok {
		t.Error("/abort should not be advertised in CLI completions until it is a real CLI command")
	}
	if _, ok := cmds["/help"]; !ok {
		t.Error("/help should be in slashCommands list")
	}
	if _, ok := cmds["/new"]; !ok {
		t.Error("/new should be in slashCommands list")
	}
	if _, ok := cmds["/compact"]; !ok {
		t.Error("/compact should be in slashCommands list")
	}
	if _, ok := cmds["/model"]; !ok {
		t.Error("/model should be in slashCommands list")
	}
	if cmd, ok := cmds["/agent"]; !ok {
		t.Error("/agent should be in slashCommands list")
	} else if !strings.Contains(cmd.description, "Channel-only") {
		t.Errorf("/agent description should explain CLI limitation, got %q", cmd.description)
	}
	if cmd, ok := cmds["/whoami"]; !ok {
		t.Error("/whoami should be in slashCommands list")
	} else if !strings.Contains(cmd.description, "CLI") {
		t.Errorf("/whoami description should explain CLI limitation, got %q", cmd.description)
	}
	if _, ok := cmds["/quit"]; !ok {
		t.Error("/quit should be in slashCommands list")
	}
	if _, ok := cmds["/exit"]; !ok {
		t.Error("/exit should be in slashCommands list")
	}
}
