package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/CherryHQ/stella/pkg/ai"
)

const dumpLLMContextEnv = "STELLA_DUMP_LLM_CONTEXT"

type dumpedLLMRequest struct {
	CreatedAt string              `json:"created_at"`
	SessionID string              `json:"session_id,omitempty"`
	AgentID   string              `json:"agent_id,omitempty"`
	UserID    string              `json:"user_id,omitempty"`
	Model     ai.Model            `json:"model"`
	System    string              `json:"system"`
	Messages  []dumpedMessage     `json:"messages"`
	Tools     []ai.ToolDefinition `json:"tools"`
	Options   dumpedStreamOptions `json:"options"`
}

type dumpedStreamOptions struct {
	BaseURL     string   `json:"base_url,omitempty"`
	MaxTokens   *int     `json:"max_tokens,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	Transport   string   `json:"transport,omitempty"`
}

type dumpedMessage struct {
	Role       string        `json:"role"`
	Content    any           `json:"content,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	ToolName   string        `json:"tool_name,omitempty"`
	IsError    bool          `json:"is_error,omitempty"`
	StopReason ai.StopReason `json:"stop_reason,omitempty"`
	Usage      *ai.Usage     `json:"usage,omitempty"`
	Blocks     []dumpedBlock `json:"blocks,omitempty"`
}

type dumpedBlock struct {
	Kind      string         `json:"kind"`
	Text      string         `json:"text,omitempty"`
	ToolID    string         `json:"tool_id,omitempty"`
	ToolName  string         `json:"tool_name,omitempty"`
	Arguments map[string]any `json:"arguments,omitempty"`
	MimeType  string         `json:"mime_type,omitempty"`
	DataLen   int            `json:"data_len,omitempty"`
	Redacted  bool           `json:"redacted,omitempty"`
}

func dumpLLMContextIfEnabled(ctx context.Context, cfg loopConfig, messages []ai.Message) error {
	dir, ok := llmDumpDir()
	if !ok {
		return nil
	}
	if cfg.OperationCheck != nil {
		if err := cfg.OperationCheck(ctx); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create LLM dump directory: %w", err)
	}
	if cfg.OperationCheck != nil {
		if err := cfg.OperationCheck(ctx); err != nil {
			return err
		}
	}

	now := time.Now().UTC()
	req := dumpedLLMRequest{
		CreatedAt: now.Format(time.RFC3339Nano),
		SessionID: cfg.HookMeta.SessionID,
		AgentID:   cfg.HookMeta.AgentID,
		UserID:    cfg.HookMeta.UserID,
		Model:     cfg.Model,
		System:    cfg.System,
		Messages:  dumpMessages(messages),
		Tools:     cfg.ToolDefinitions,
		Options: dumpedStreamOptions{
			BaseURL:     cfg.Model.BaseURL,
			MaxTokens:   cfg.StreamOptions.MaxTokens,
			Temperature: cfg.StreamOptions.Temperature,
			Transport:   cfg.StreamOptions.Transport,
		},
	}

	data, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return fmt.Errorf("encode LLM context dump: %w", err)
	}
	name := fmt.Sprintf("%s-%s.json", now.Format("20060102T150405.000000000Z"), safeName(cfg.HookMeta.SessionID))
	if cfg.OperationCheck != nil {
		if err := cfg.OperationCheck(ctx); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		return fmt.Errorf("write LLM context dump: %w", err)
	}
	if cfg.OperationCheck != nil {
		if err := cfg.OperationCheck(ctx); err != nil {
			return err
		}
	}
	return nil
}

func llmDumpDir() (string, bool) {
	value := strings.TrimSpace(os.Getenv(dumpLLMContextEnv))
	if value == "" || value == "0" || strings.EqualFold(value, "false") {
		return "", false
	}
	if value != "1" && !strings.EqualFold(value, "true") {
		return value, true
	}
	stellaHome := os.Getenv("STELLA_HOME")
	if stellaHome == "" {
		if home, err := os.UserHomeDir(); err == nil {
			stellaHome = filepath.Join(home, ".stella")
		}
	}
	return filepath.Join(stellaHome, "debug", "llm-requests"), true
}

func dumpMessages(messages []ai.Message) []dumpedMessage {
	out := make([]dumpedMessage, 0, len(messages))
	for _, msg := range messages {
		switch m := msg.(type) {
		case ai.UserMessage:
			out = append(out, dumpedMessage{Role: "user", Content: dumpUserContent(m.TimestampedContent())})
		case ai.AssistantMessage:
			dm := dumpedMessage{Role: "assistant", Blocks: dumpBlocks(m.Content), StopReason: m.StopReason}
			if m.Usage != (ai.Usage{}) {
				usage := m.Usage
				dm.Usage = &usage
			}
			out = append(out, dm)
		case ai.ToolResultMessage:
			out = append(out, dumpedMessage{Role: "tool", ToolCallID: m.ToolCallID, ToolName: m.ToolName, Blocks: dumpBlocks(m.Content), IsError: m.IsError})
		default:
			out = append(out, dumpedMessage{Role: "unknown", Content: fmt.Sprintf("%T", msg)})
		}
	}
	return out
}

func dumpUserContent(content any) any {
	if blocks, ok := content.([]ai.ContentBlock); ok {
		return dumpBlocks(blocks)
	}
	return content
}

func dumpBlocks(blocks []ai.ContentBlock) []dumpedBlock {
	out := make([]dumpedBlock, 0, len(blocks))
	for _, block := range blocks {
		switch b := block.(type) {
		case ai.TextContent:
			out = append(out, dumpedBlock{Kind: "text", Text: b.Text})
		case ai.ThinkingContent:
			out = append(out, dumpedBlock{Kind: "thinking", Text: b.Thinking, Redacted: b.Redacted})
		case ai.ToolCall:
			out = append(out, dumpedBlock{Kind: "tool_call", ToolID: b.ID, ToolName: b.Name, Arguments: b.Arguments})
		case ai.ImageContent:
			out = append(out, dumpedBlock{Kind: "image", MimeType: b.MimeType, DataLen: len(b.Data)})
		default:
			out = append(out, dumpedBlock{Kind: fmt.Sprintf("%T", block)})
		}
	}
	return out
}

func safeName(s string) string {
	if s == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}
