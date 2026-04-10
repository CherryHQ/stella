package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vaayne/anna/internal/agent"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
)

// ModelOption re-exports pkg/channel.ModelOption for use by callers.
type ModelOption = pkgchannel.ModelOption

// ModelListFunc re-exports the model-list callback shape for use by callers.
type ModelListFunc = func() []pkgchannel.ModelOption

// ModelSwitchFunc re-exports the model-switch callback shape for use by callers.
type ModelSwitchFunc = func(provider, model string) error

const defaultStreamSessionId = "stream"

// RunStream reads all of stdin as a prompt, sends it to the agent, and streams the response to stdout.
// An optional userID associates the session with a user.
func RunStream(ctx context.Context, pool *agent.Pool, userID ...int64) error {
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("reading stdin: %w", err)
	}

	prompt := strings.TrimSpace(string(input))
	if prompt == "" {
		return fmt.Errorf("empty prompt")
	}

	// Ensure session exists with user association.
	info, _ := pool.ResolveSession(defaultStreamSessionId, userID...)
	sessionID := info.ID
	if sessionID == "" {
		sessionID = defaultStreamSessionId
	}

	stream := pool.Chat(ctx, sessionID, prompt)
	for evt := range stream {
		if evt.Err != nil {
			return evt.Err
		}
		fmt.Print(evt.Text)
	}
	fmt.Println()
	return nil
}

// RunChat starts an interactive terminal chat session using Bubble Tea.
// An optional userID associates sessions with a user for per-user memory.
func RunChat(ctx context.Context, pool *agent.Pool, provider, model string, listFn ModelListFunc, switchFn ModelSwitchFunc, userID ...int64) error {
	m := newChatModel(ctx, pool, provider, model, listFn, switchFn, userID...)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}
	return nil
}
