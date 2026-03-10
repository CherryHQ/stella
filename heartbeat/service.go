package heartbeat

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/vaayne/anna/agent/runner"
	"github.com/vaayne/anna/channel"
)

const (
	decisionSessionID = "heartbeat:decision"
	mainSessionID     = "heartbeat:main"
)

type ChatFunc func(ctx context.Context, sessionID, message, model string) <-chan runner.Event

type Config struct {
	File      string
	FastModel string
}

type Service struct {
	cfg      Config
	chat     ChatFunc
	notifier channel.Notifier
	now      func() time.Time
	log      *slog.Logger
}

type Decision struct {
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
}

func New(cfg Config, chat ChatFunc, notifier channel.Notifier) *Service {
	return &Service{
		cfg:      cfg,
		chat:     chat,
		notifier: notifier,
		now:      time.Now,
		log:      slog.With("component", "heartbeat"),
	}
}

func (s *Service) Poll(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("heartbeat service is nil")
	}
	if s.chat == nil {
		return fmt.Errorf("heartbeat chat function is nil")
	}

	content, err := s.readHeartbeatFile()
	if err != nil {
		return err
	}
	if content == "" {
		return nil
	}

	decision, err := s.decide(ctx, content)
	if err != nil {
		return err
	}
	if decision.Action == "skip" {
		s.log.Debug("heartbeat skipped", "reason", decision.Reason)
		return nil
	}

	result, err := s.execute(ctx, content)
	if err != nil {
		return err
	}
	result = strings.TrimSpace(result)
	if result == "" {
		return nil
	}

	text := "*Heartbeat*\n\n" + result
	if err := s.notifier.Notify(ctx, channel.Notification{Text: text}); err != nil {
		return fmt.Errorf("notify heartbeat result: %w", err)
	}

	s.log.Info("heartbeat executed", "reason", decision.Reason)
	return nil
}

func (s *Service) readHeartbeatFile() (string, error) {
	if strings.TrimSpace(s.cfg.File) == "" {
		return "", fmt.Errorf("heartbeat file is not configured")
	}

	data, err := os.ReadFile(s.cfg.File)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read heartbeat file: %w", err)
	}

	return strings.TrimSpace(string(data)), nil
}

func (s *Service) decide(ctx context.Context, content string) (Decision, error) {
	prompt := buildDecisionPrompt(s.now().UTC(), content)
	text, usedTools, err := s.collect(ctx, decisionSessionID, prompt, s.cfg.FastModel)
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

func (s *Service) execute(ctx context.Context, content string) (string, error) {
	prompt := buildExecutionPrompt(s.now().UTC(), content)
	text, _, err := s.collect(ctx, mainSessionID, prompt, "")
	if err != nil {
		return "", err
	}
	return text, nil
}

func (s *Service) collect(ctx context.Context, sessionID, message, model string) (string, bool, error) {
	stream := s.chat(ctx, sessionID, message, model)
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
