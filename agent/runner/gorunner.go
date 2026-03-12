package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/vaayne/anna/agent/engine"
	"github.com/vaayne/anna/agent/tool"
	"github.com/vaayne/anna/ai"
	"github.com/vaayne/anna/ai/providers/anthropic"
	"github.com/vaayne/anna/ai/providers/openai"
	openairesponse "github.com/vaayne/anna/ai/providers/openai-response"
)

const maxToolIterations = 40

// GoRunnerConfig configures the Go runner.
type GoRunnerConfig struct {
	API        string // provider key: "anthropic", "openai"
	Model      string // e.g. "claude-sonnet-4-20250514"
	APIKey     string
	BaseURL    string      // optional provider base URL override
	WorkDir    string      // working directory for tool execution
	Workspace  string      // workspace dir for skills/memory (e.g. ~/.anna/workspace)
	AnnaHome   string      // anna home directory (e.g. ~/.anna)
	System     string      // optional system prompt override (bypasses BuildSystemPrompt)
	ExtraTools []tool.Tool // additional tools to register
}

// GoRunner implements Runner by calling LLM providers directly via Engine.
type GoRunner struct {
	eng    *engine.Engine
	reg    *ai.Registry
	tools  *tool.Registry
	model  ai.Model
	apiKey string
	system string

	mu           sync.Mutex
	lastActivity time.Time
	log          *slog.Logger
}

// NewGoRunner creates a Go runner with built-in providers.
func NewGoRunner(_ context.Context, cfg GoRunnerConfig) (*GoRunner, error) {
	if cfg.API == "" {
		return nil, fmt.Errorf("go runner: api is required")
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("go runner: model is required")
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("go runner: api_key is required")
	}

	reg := ai.NewRegistry()
	reg.Register(anthropic.New(anthropic.Config{BaseURL: cfg.BaseURL}))
	reg.Register(openai.New(openai.Config{BaseURL: cfg.BaseURL}))
	reg.Register(openairesponse.New(openairesponse.Config{BaseURL: cfg.BaseURL}))

	system := cfg.System
	if system == "" {
		if cfg.Workspace != "" {
			system = BuildSystemPrompt(cfg.AnnaHome, cfg.Workspace, cfg.WorkDir)
		} else {
			system = defaultBasicPrompt
		}
	}

	tools := tool.NewRegistry(cfg.WorkDir)
	for _, t := range cfg.ExtraTools {
		tools.Register(t)
	}

	return &GoRunner{
		eng:          &engine.Engine{Providers: reg},
		reg:          reg,
		tools:        tools,
		model:        ai.Model{API: cfg.API, Name: cfg.Model},
		apiKey:       cfg.APIKey,
		system:       system,
		lastActivity: time.Now(),
		log:          slog.With("component", "go_runner"),
	}, nil
}

// Chat converts history, runs the Engine agent loop, and forwards events to the returned channel.
func (r *GoRunner) Chat(ctx context.Context, history []RPCEvent, message MessageContent) <-chan Event {
	out := make(chan Event, 100)

	r.mu.Lock()
	r.lastActivity = time.Now()
	r.mu.Unlock()

	go func() {
		defer close(out)

		messages := convertHistory(history)
		messages = append(messages, ai.UserMessage{Content: message})

		cfg := engine.LoopConfig{
			Model:           r.model,
			StreamOptions:   ai.StreamOptions{APIKey: r.apiKey},
			MaxTurns:        maxToolIterations,
			Tools:           r.buildToolSet(),
			ToolDefinitions: r.tools.Definitions(),
			System:          r.system,
		}

		if _, err := r.eng.Run(ctx, cfg, messages, func(e engine.LoopEvent) {
			for _, evt := range convertLoopEvent(e) {
				out <- evt
			}
		}); err != nil {
			out <- Event{Err: err}
		}
	}()

	return out
}

// Alive always returns true — the Go runner has no subprocess to die.
func (r *GoRunner) Alive() bool { return true }

// LastActivity returns the time of the last Chat call.
func (r *GoRunner) LastActivity() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastActivity
}

// Close is a no-op for the Go runner.
func (r *GoRunner) Close() error { return nil }

// buildToolSet adapts tool.Registry to engine.ToolSet for Engine.
func (r *GoRunner) buildToolSet() engine.ToolSet {
	set := engine.ToolSet{}
	for _, def := range r.tools.Definitions() {
		name := def.Name
		set[name] = func(ctx context.Context, call ai.ToolCall) (ai.TextContent, error) {
			result, err := r.tools.Execute(ctx, name, call.Arguments)
			return ai.TextContent{Text: result}, err
		}
	}
	return set
}

// convertLoopEvent bridges engine.LoopEvent to Event(s).
func convertLoopEvent(e engine.LoopEvent) []Event {
	switch e := e.(type) {
	case engine.AssistantDelta:
		if d, ok := e.Event.(ai.EventTextDelta); ok && d.Text != "" {
			return []Event{{Text: d.Text}}
		}

	case engine.AssistantFinished:
		// Emit Store events for tool calls in the final message.
		var events []Event
		for _, block := range e.Message.Content {
			if call, ok := block.(ai.ToolCall); ok {
				rpc := ToolCallToRPCEvent(call)
				events = append(events, Event{Store: &rpc})
			}
		}
		return events

	case engine.ToolStarted:
		return []Event{{ToolUse: &ToolUseEvent{
			Tool:   e.ToolCall.Name,
			Status: "running",
			Input:  summarizeToolInput(e.ToolCall.Name, e.ToolCall.Arguments),
		}}}

	case engine.ToolFinished:
		status := "done"
		detail := summarizeToolResult(e.Result)
		if e.Result.IsError {
			status = "error"
		}
		rpc := ToolResultToRPCEvent(e.Result)
		return []Event{
			{ToolUse: &ToolUseEvent{
				Tool:   e.Result.ToolName,
				Status: status,
				Detail: detail,
			}},
			{Store: &rpc},
		}

	case engine.AgentErrored:
		return []Event{{Err: e.Err}}
	}

	return nil
}

// summarizeToolResult returns a short human-readable summary of a tool result.
func summarizeToolResult(result ai.ToolResultMessage) string {
	var text string
	for _, block := range result.Content {
		if tc, ok := block.(ai.TextContent); ok {
			text = tc.Text
			break
		}
	}
	if text == "" {
		return ""
	}

	if result.IsError {
		// For errors, show the first line.
		if idx := strings.Index(text, "\n"); idx > 0 {
			text = text[:idx]
		}
		if len(text) > 120 {
			return text[:117] + "..."
		}
		return text
	}

	// For success, produce a brief summary based on tool type.
	lines := strings.Count(text, "\n") + 1
	runeCount := len([]rune(text))

	switch {
	case runeCount <= 80:
		// Short result — show inline.
		if idx := strings.Index(text, "\n"); idx > 0 {
			text = text[:idx]
		}
		return text
	case lines > 1:
		return fmt.Sprintf("%d lines", lines)
	default:
		return fmt.Sprintf("%d chars", runeCount)
	}
}

// summarizeToolInput returns a short human-readable summary of tool arguments.
func summarizeToolInput(toolName string, args map[string]any) string {
	switch toolName {
	case "bash":
		if cmd, ok := args["command"].(string); ok {
			if len(cmd) > 80 {
				return cmd[:80] + "..."
			}
			return cmd
		}
	case "read":
		if fp, ok := args["file_path"].(string); ok {
			return fp
		}
	case "write":
		if fp, ok := args["file_path"].(string); ok {
			return fp
		}
	case "edit":
		if fp, ok := args["file_path"].(string); ok {
			return fp
		}
	}
	return ""
}

// decodeUserContent reconstructs the user message content from an RPCEvent.
// Returns []ai.ContentBlock if the event has multimodal content, or string otherwise.
func decodeUserContent(evt RPCEvent) any {
	if len(evt.Content) == 0 {
		return evt.Summary
	}
	var blocks []ContentBlockJSON
	if err := json.Unmarshal(evt.Content, &blocks); err != nil {
		return evt.Summary
	}
	content := make([]ai.ContentBlock, 0, len(blocks))
	for _, b := range blocks {
		switch b.Kind {
		case BlockKindText:
			content = append(content, ai.TextContent{Text: b.Text})
		case BlockKindImage:
			content = append(content, ai.ImageContent{Data: b.Data, MimeType: b.MimeType})
		}
	}
	if len(content) == 0 {
		return evt.Summary
	}
	return content
}

// convertHistory rebuilds []ai.Message from RPCEvent history.
func convertHistory(events []RPCEvent) []ai.Message {
	var messages []ai.Message
	var textBuf string
	var pendingCalls []ai.ToolCall
	seenCallIDs := map[string]bool{}

	flush := func() {
		if textBuf != "" {
			messages = append(messages, ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: textBuf}},
			})
			textBuf = ""
		}
	}

	flushToolCalls := func() {
		if len(pendingCalls) > 0 {
			blocks := make([]ai.ContentBlock, 0, len(pendingCalls)+1)
			if textBuf != "" {
				blocks = append(blocks, ai.TextContent{Text: textBuf})
				textBuf = ""
			}
			for _, c := range pendingCalls {
				blocks = append(blocks, c)
			}
			messages = append(messages, ai.AssistantMessage{Content: blocks})
			pendingCalls = nil
		}
	}

	for _, evt := range events {
		switch evt.Type {
		case RPCEventUserMessage:
			flushToolCalls()
			flush()
			messages = append(messages, ai.UserMessage{Content: decodeUserContent(evt)})

		case RPCEventMessageUpdate:
			if evt.Summary != "" {
				textBuf += evt.Summary
			} else if len(evt.AssistantMessageEvent) > 0 {
				var ame AssistantMessageEvent
				if json.Unmarshal(evt.AssistantMessageEvent, &ame) == nil && ame.Type == "text_delta" {
					textBuf += ame.Delta
				}
			}

		case RPCEventToolCall:
			var args map[string]any
			_ = json.Unmarshal(evt.Result, &args)
			seenCallIDs[evt.ID] = true
			pendingCalls = append(pendingCalls, ai.ToolCall{
				ID:        evt.ID,
				Name:      evt.Tool,
				Arguments: args,
			})

		case RPCEventToolResult:
			// Skip orphaned tool results (no matching tool call).
			if !seenCallIDs[evt.ID] {
				continue
			}
			flushToolCalls()
			var content string
			_ = json.Unmarshal(evt.Result, &content)
			messages = append(messages, ai.ToolResultMessage{
				ToolCallID: evt.ID,
				ToolName:   evt.Tool,
				Content:    []ai.ContentBlock{ai.TextContent{Text: content}},
				IsError:    evt.Error != "",
			})
		}
	}

	flushToolCalls()
	flush()
	return messages
}
