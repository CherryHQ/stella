package main

import (
	"sort"
	"time"
)

// Metrics answer the review question a reward alone cannot: how long the agent
// worked, how many turns and tool calls it took, where the time went, and how
// many tokens it burned. They are derived from the session's own message
// timeline, so they describe the product's behaviour rather than the harness's.
// usage is what the provider actually reported, fetched from Stella's session
// usage API. Every total is a pointer: null means "not reported" or "not
// priced", which is a different fact from zero and must not be flattened into
// one. A leaderboard reads these as ground truth.
type usage struct {
	PendingCallCount  *int64   `json:"pending_call_count,omitempty"`
	CallCount         int64    `json:"call_count"`
	ReportedCallCount int64    `json:"reported_call_count"`
	PricedCallCount   int64    `json:"priced_call_count"`
	InputTokens       *int64   `json:"input_tokens"`
	OutputTokens      *int64   `json:"output_tokens"`
	CacheReadTokens   *int64   `json:"cache_read_tokens"`
	CacheWriteTokens  *int64   `json:"cache_write_tokens"`
	CostUSD           *float64 `json:"cost_usd"`
}

type metrics struct {
	Turns         int `json:"turns"`
	ToolCallTotal int `json:"tool_call_total"`
	// ToolErrorTotal counts calls where the tool itself failed. A command that
	// ran and exited nonzero is not one of those: it is the container
	// answering, and it is counted in CommandNonzeroTotal instead. Keeping
	// them in one number made a clean run report dozens of "errors" and fed a
	// failure taxonomy that a reader would trust.
	ToolErrorTotal      int                 `json:"tool_error_total"`
	CommandNonzeroTotal int                 `json:"command_nonzero_total"`
	Tools               map[string]toolStat `json:"tools"`
	SlowestToolCall     *toolCallTiming     `json:"slowest_tool_call,omitempty"`
	Tokens              tokenBreakdown      `json:"tokens_estimated"`
	Usage               *usage              `json:"usage,omitempty"`
	Timing              timing              `json:"timing_ms"`
}

type toolStat struct {
	Calls int `json:"calls"`
	// Errors and CommandNonzero split the same way the totals do.
	Errors         int   `json:"errors"`
	CommandNonzero int   `json:"command_nonzero"`
	TotalMs        int64 `json:"total_ms"`
	MaxMs          int64 `json:"max_ms"`
}

type toolCallTiming struct {
	Name string `json:"name"`
	Ms   int64  `json:"ms"`
}

// tokenBreakdown reports what the sessions API exposes: one count per message,
// and that count is an estimate (memory.EstimateTokens, len/4), not provider
// usage. It is useful for comparing trials against each other and useless for
// cost. Real prompt/output/cache accounting needs Stella to expose per-call
// usage; nothing here is a substitute for it.
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
	// ErrorKind is the server's own classification of IsError. A server that
	// predates it sends nothing, and an absent kind stays an unclassified tool
	// error: the driver never re-derives it from the message text.
	ErrorKind  string          `json:"error_kind"`
	ChildCalls []childToolCall `json:"child_calls"`
	Blocks     []struct {
		Type      string         `json:"type"`
		ID        string         `json:"id"`
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	} `json:"blocks"`
}

// errorKindCommandNonzero is the sessions API's name for "the command ran and
// exited nonzero". Any other value, including an absent one, is a tool error.
const errorKindCommandNonzero = "command_nonzero"

// deriveMetrics walks the message timeline once. It is pure so the attribution
// rules stay testable without a server.
func deriveMetrics(messages []sessionMessage) (metrics, []toolCall) {
	m := metrics{Tools: map[string]toolStat{}}
	var calls []toolCall
	pending := map[string]struct {
		name  string
		at    time.Time
		index int
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
					name  string
					at    time.Time
					index int
				}{b.Name, msg.Timestamp, len(calls) - 1}
			}
		}

		if msg.Role == "tool" {
			commandNonzero := msg.IsError && msg.ErrorKind == errorKindCommandNonzero
			if msg.IsError {
				if commandNonzero {
					m.CommandNonzeroTotal++
				} else {
					m.ToolErrorTotal++
				}
			}
			if call, ok := pending[msg.ToolCallID]; ok {
				delete(pending, msg.ToolCallID)
				elapsed := millisBetween(call.at, msg.Timestamp)
				m.Timing.ToolMs += elapsed
				stat := m.Tools[call.name]
				if msg.IsError {
					if commandNonzero {
						stat.CommandNonzero++
					} else {
						stat.Errors++
					}
					// The call did not succeed either way; the evidence
					// predicate cares about that, not about whose fault it was.
					calls[call.index].IsError = true
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
