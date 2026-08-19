package main

import (
	"sort"
	"time"
)

// Metrics answer the review question a reward alone cannot: how long the agent
// worked, how many turns and tool calls it took, where the time went, and how
// many tokens it burned. They are derived from the session's own message
// timeline, so they describe the product's behaviour rather than the harness's.
type metrics struct {
	Turns           int                 `json:"turns"`
	ToolCallTotal   int                 `json:"tool_call_total"`
	ToolErrorTotal  int                 `json:"tool_error_total"`
	Tools           map[string]toolStat `json:"tools"`
	SlowestToolCall *toolCallTiming     `json:"slowest_tool_call,omitempty"`
	Tokens          tokenBreakdown      `json:"tokens"`
	Timing          timing              `json:"timing_ms"`
}

type toolStat struct {
	Calls   int   `json:"calls"`
	Errors  int   `json:"errors"`
	TotalMs int64 `json:"total_ms"`
	MaxMs   int64 `json:"max_ms"`
}

type toolCallTiming struct {
	Name string `json:"name"`
	Ms   int64  `json:"ms"`
}

// tokenBreakdown reports what the sessions API exposes: one count per message.
// Stella does not surface the provider's input/output/cache split, so a
// prompt/completion breakdown would have to be invented and is left out.
type tokenBreakdown struct {
	Total     int64 `json:"total"`
	User      int64 `json:"user"`
	Assistant int64 `json:"assistant"`
	Tool      int64 `json:"tool"`
}

// timing splits wall-clock time into the phases a reviewer can act on. Model
// and tool time are attributed from message timestamps: the gap before an
// assistant message is time the model held, the gap before a tool result is
// time that tool held. Anything the driver spent outside the turn lands in the
// setup, stop, and export phases instead.
type timing struct {
	TotalMs         int64 `json:"total"`
	ProvisionMs     int64 `json:"provision"`
	SetupMs         int64 `json:"setup"`
	TurnMs          int64 `json:"turn"`
	StopMs          int64 `json:"stop"`
	ExportMs        int64 `json:"export"`
	FirstResponseMs int64 `json:"first_response"`
	ModelMs         int64 `json:"model"`
	ToolMs          int64 `json:"tool"`
}

type sessionMessage struct {
	Role       string    `json:"role"`
	TokenCount int64     `json:"token_count"`
	Timestamp  time.Time `json:"timestamp"`
	ToolCallID string    `json:"tool_call_id"`
	IsError    bool      `json:"is_error"`
	Blocks     []struct {
		Type      string         `json:"type"`
		ID        string         `json:"id"`
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	} `json:"blocks"`
}

// deriveMetrics walks the message timeline once. It is pure so the attribution
// rules stay testable without a server.
func deriveMetrics(messages []sessionMessage) (metrics, []toolCall) {
	m := metrics{Tools: map[string]toolStat{}}
	var calls []toolCall
	pending := map[string]struct {
		name string
		at   time.Time
	}{}
	var prev time.Time
	var firstUser time.Time

	for _, msg := range messages {
		switch msg.Role {
		case "user":
			if firstUser.IsZero() {
				firstUser = msg.Timestamp
			}
			m.Tokens.User += msg.TokenCount
		case "assistant":
			m.Turns++
			m.Tokens.Assistant += msg.TokenCount
			if !prev.IsZero() && !msg.Timestamp.IsZero() {
				m.Timing.ModelMs += millisBetween(prev, msg.Timestamp)
			}
			if m.Timing.FirstResponseMs == 0 && !firstUser.IsZero() && !msg.Timestamp.IsZero() {
				m.Timing.FirstResponseMs = millisBetween(firstUser, msg.Timestamp)
			}
		case "tool":
			m.Tokens.Tool += msg.TokenCount
		}

		for _, b := range msg.Blocks {
			if b.Type != "tool_call" || b.Name == "" {
				continue
			}
			m.ToolCallTotal++
			stat := m.Tools[b.Name]
			stat.Calls++
			m.Tools[b.Name] = stat
			calls = append(calls, toolCall{Name: b.Name, Arguments: b.Arguments})
			if b.ID != "" {
				pending[b.ID] = struct {
					name string
					at   time.Time
				}{b.Name, msg.Timestamp}
			}
		}

		if msg.Role == "tool" {
			if msg.IsError {
				m.ToolErrorTotal++
			}
			if call, ok := pending[msg.ToolCallID]; ok {
				delete(pending, msg.ToolCallID)
				elapsed := millisBetween(call.at, msg.Timestamp)
				m.Timing.ToolMs += elapsed
				stat := m.Tools[call.name]
				if msg.IsError {
					stat.Errors++
				}
				stat.TotalMs += elapsed
				if elapsed > stat.MaxMs {
					stat.MaxMs = elapsed
				}
				m.Tools[call.name] = stat
				if m.SlowestToolCall == nil || elapsed > m.SlowestToolCall.Ms {
					m.SlowestToolCall = &toolCallTiming{Name: call.name, Ms: elapsed}
				}
			}
		}
		if !msg.Timestamp.IsZero() {
			prev = msg.Timestamp
		}
	}
	m.Tokens.Total = m.Tokens.User + m.Tokens.Assistant + m.Tokens.Tool
	return m, calls
}

// millisBetween never reports negative time: messages written in the same
// transaction can share or invert a timestamp, and a negative duration in a
// report is worse than a zero.
func millisBetween(from, to time.Time) int64 {
	d := to.Sub(from).Milliseconds()
	if d < 0 {
		return 0
	}
	return d
}

func toolNames(tools map[string]toolStat) []string {
	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
