package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/notify"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

const (
	heartbeatDecisionSessionID = "heartbeat:decision"
	heartbeatMainSessionID     = "heartbeat:main"
)

// ChatFunc streams runner events for heartbeat decision/execution prompts.
type ChatFunc func(ctx context.Context, sessionID, message, model string) <-chan agent.Event

// HeartbeatConfig holds heartbeat-specific settings.
type HeartbeatConfig struct {
	File      string
	FastModel string
}

// Decision is the gate-keeper response from the LLM.
type Decision struct {
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
}

// SetHeartbeat configures the heartbeat on the scheduler service.
func (s *Service) SetHeartbeat(cfg HeartbeatConfig, chat ChatFunc, notifier notify.Notifier) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.heartbeatCfg = &cfg
	s.heartbeatChat = chat
	s.heartbeatNotifier = notifier
}

// StartHeartbeat schedules the heartbeat poll on the shared scheduler.
func (s *Service) StartHeartbeat(ctx context.Context, every string) error {
	s.mu.Lock()
	cfg := s.heartbeatCfg
	s.mu.Unlock()

	if cfg == nil {
		return fmt.Errorf("heartbeat not configured; call SetHeartbeat first")
	}

	s.log.Info("starting heartbeat", "every", every)
	return s.ScheduleEvery(ctx, every, func(ctx context.Context) {
		if err := s.heartbeatPoll(ctx); err != nil {
			s.log.Error("heartbeat poll failed", "error", err)
		}
	})
}

// heartbeatPoll reads the heartbeat file, decides whether to act, executes,
// and sends the result via the notifier.
func (s *Service) heartbeatPoll(ctx context.Context) error {
	s.mu.Lock()
	cfg := s.heartbeatCfg
	chat := s.heartbeatChat
	notifier := s.heartbeatNotifier
	s.mu.Unlock()

	if cfg == nil {
		return fmt.Errorf("heartbeat not configured")
	}
	if chat == nil {
		return fmt.Errorf("heartbeat chat function is nil")
	}

	content, err := readHeartbeatFile(cfg.File)
	if err != nil {
		return err
	}
	if content == "" {
		return nil
	}

	decision, err := heartbeatDecide(ctx, chat, cfg.FastModel, content)
	if err != nil {
		return err
	}
	if decision.Action == "skip" {
		s.log.Debug("heartbeat skipped", "reason", decision.Reason)
		return nil
	}

	result, err := heartbeatExecute(ctx, chat, content)
	if err != nil {
		return err
	}
	result = strings.TrimSpace(result)
	if result == "" {
		return nil
	}

	text := "*Heartbeat*\n\n" + result
	if err := notifier.Notify(ctx, pkgchannel.Notification{Text: text}); err != nil {
		return fmt.Errorf("notify heartbeat result: %w", err)
	}

	s.log.Info("heartbeat executed", "reason", decision.Reason)
	return nil
}

func readHeartbeatFile(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("heartbeat file is not configured")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read heartbeat file: %w", err)
	}

	return strings.TrimSpace(string(data)), nil
}

func heartbeatDecide(ctx context.Context, chat ChatFunc, fastModel, content string) (Decision, error) {
	prompt := buildDecisionPrompt(time.Now().UTC(), content)
	text, usedTools, err := collectChat(ctx, chat, heartbeatDecisionSessionID, prompt, fastModel)
	if err != nil {
		return Decision{}, err
	}
	if usedTools {
		return Decision{}, fmt.Errorf("heartbeat decision attempted to use tools")
	}

	decision, err := parseDecision(text)
	if err != nil {
		return Decision{}, fmt.Errorf("parse heartbeat decision: %w", err)
	}
	return decision, nil
}

func heartbeatExecute(ctx context.Context, chat ChatFunc, content string) (string, error) {
	prompt := buildExecutionPrompt(time.Now().UTC(), content)
	text, _, err := collectChat(ctx, chat, heartbeatMainSessionID, prompt, "")
	if err != nil {
		return "", err
	}
	return text, nil
}

func collectChat(ctx context.Context, chat ChatFunc, sessionID, message, model string) (string, bool, error) {
	stream := chat(ctx, sessionID, message, model)
	var buf strings.Builder
	usedTools := false

	for evt := range stream {
		if evt.Err != nil {
			return buf.String(), usedTools, evt.Err
		}
		if evt.ToolUse != nil {
			usedTools = true
		}
		if evt.Text != "" {
			buf.WriteString(evt.Text)
		}
	}

	return buf.String(), usedTools, nil
}

func parseDecision(raw string) (Decision, error) {
	var decision Decision
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &decision); err != nil {
		return Decision{}, err
	}

	decision.Action = strings.ToLower(strings.TrimSpace(decision.Action))
	switch decision.Action {
	case "skip", "run":
		return decision, nil
	default:
		return Decision{}, fmt.Errorf("unsupported action %q", decision.Action)
	}
}

func buildDecisionPrompt(now time.Time, content string) string {
	return fmt.Sprintf(`You are the heartbeat gate for an autonomous assistant.
Decide whether the heartbeat instructions need action right now.

Rules:
- Return JSON only.
- Do not call any tools.
- Use exactly one of the following actions: "skip" or "run".
- Keep the reason short.

Response schema:
{"action":"skip","reason":"..."}

Current time (UTC): %s

HEARTBEAT.md:
%s`, now.Format(time.RFC3339), content)
}

func buildExecutionPrompt(now time.Time, content string) string {
	return fmt.Sprintf(`[Heartbeat Trigger]
Current time (UTC): %s

The heartbeat gate already decided this requires action now.
Follow the instructions below.
Do not use the notify tool. Your final response will be delivered automatically.

HEARTBEAT.md:
%s`, now.Format(time.RFC3339), content)
}
