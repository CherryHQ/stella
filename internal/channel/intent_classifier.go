package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
)

type Intent string

const (
	IntentHelp    Intent = "help"
	IntentNew     Intent = "new"
	IntentAbort   Intent = "abort"
	IntentCompact Intent = "compact"
	IntentNone    Intent = "none"
)

const (
	maxIntentRunes = 32
	maxIntentWords = 6
)

const intentClassifierPrompt = `You classify very short user messages into chat control actions.
Return JSON only in the form {"action":"help|new|abort|compact|none"}.

Choose an action only when the user is clearly asking for chat control.
If the message could reasonably be a normal chat request, return {"action":"none"}.

Action meanings:
- help: user wants help, available commands, or usage instructions
- new: user wants a CLEAN SLATE — start a new session, start over, forget/clear the current context
- abort: user wants to cancel/stop the in-progress response
- compact: user wants to KEEP this conversation but SHORTEN it — compact, compress, or summarize the history
- none: anything else

new and compact are different requests. "new" throws the current context away;
"compact" keeps the same conversation and only condenses it. Never map one to the
other.

Examples:
"help" -> {"action":"help"}
"what can you do" -> {"action":"help"}
"new session" -> {"action":"new"}
"start over" -> {"action":"new"}
"clear context" -> {"action":"new"}
"forget everything" -> {"action":"new"}
"cancel" -> {"action":"abort"}
"abort" -> {"action":"abort"}
"compact chat" -> {"action":"compact"}
"compress this chat" -> {"action":"compact"}
"summarize history" -> {"action":"compact"}
"帮助" -> {"action":"help"}
"新会话" -> {"action":"new"}
"开个新会话" -> {"action":"new"}
"重新开始" -> {"action":"new"}
"清除上下文" -> {"action":"new"}
"取消" -> {"action":"abort"}
"停止回复" -> {"action":"abort"}
"压缩会话" -> {"action":"compact"}
"压缩一下上下文" -> {"action":"compact"}
"总结历史" -> {"action":"compact"}
"请帮我重新开始设计数据库" -> {"action":"none"}
"how do I cancel a job?" -> {"action":"none"}`

type (
	SnapshotLoader    func(context.Context, string) (*config.Snapshot, error)
	StreamFuncBuilder func(context.Context, string, config.ProviderCreds) (providers.StreamFunc, error)
	CompleteFunc      func(context.Context, ai.Model, ai.Context, ai.CompleteOptions, providers.StreamFunc) (ai.AssistantMessage, error)
)

type IntentClassifier interface {
	Classify(ctx context.Context, agentID string, content []ai.ContentBlock) Intent
}

type LLMIntentClassifier struct {
	loadSnapshot SnapshotLoader
	buildStream  StreamFuncBuilder
	complete     CompleteFunc
	timeout      time.Duration
	log          *slog.Logger
}

func NewLLMIntentClassifier(loadSnapshot SnapshotLoader, buildStream StreamFuncBuilder) *LLMIntentClassifier {
	return &LLMIntentClassifier{
		loadSnapshot: loadSnapshot,
		buildStream:  buildStream,
		complete:     providers.Complete,
		timeout:      1500 * time.Millisecond,
		log:          slog.With("component", "intent_classifier"),
	}
}

func (c *LLMIntentClassifier) Classify(ctx context.Context, agentID string, content []ai.ContentBlock) Intent {
	text, ok := classifyCandidateText(content)
	if !ok || agentID == "" || c == nil || c.loadSnapshot == nil || c.buildStream == nil || c.complete == nil {
		return IntentNone
	}

	caller := fastModelCaller{load: c.loadSnapshot, build: c.buildStream, complete: c.complete}
	raw, stage, err := caller.Complete(ctx, agentID, intentClassifierPrompt, text, c.timeout)
	if err != nil {
		// Any failure means the message was not a control request as far as
		// this turn is concerned: it goes to the agent as ordinary chat.
		c.debug("intent classification unavailable", "agent_id", agentID, "stage", stage, "error", err)
		return IntentNone
	}

	intent, err := parseIntentResponse(raw)
	if err != nil {
		c.debug("intent response parse failed", "agent_id", agentID, "error", err)
		return IntentNone
	}
	return intent
}

func (c *LLMIntentClassifier) debug(msg string, args ...any) {
	if c != nil && c.log != nil {
		c.log.Debug(msg, args...)
	}
}

func classifyCandidateText(content []ai.ContentBlock) (string, bool) {
	candidate := content
	for len(candidate) > 0 && isIntentSystemPrefix(candidate[0]) {
		candidate = candidate[1:]
	}
	if len(candidate) != 1 {
		return "", false
	}
	textBlock, ok := candidate[0].(ai.TextContent)
	if !ok {
		return "", false
	}
	text := strings.TrimSpace(textBlock.Text)
	if text == "" || strings.Contains(text, "\n") {
		return "", false
	}
	if utf8.RuneCountInString(text) > maxIntentRunes {
		return "", false
	}
	if len(strings.Fields(text)) > maxIntentWords {
		return "", false
	}
	return text, true
}

func isIntentSystemPrefix(block ai.ContentBlock) bool {
	textBlock, ok := block.(ai.TextContent)
	if !ok {
		return false
	}
	text := strings.TrimSpace(textBlock.Text)
	return strings.HasPrefix(text, "[System:") && strings.HasSuffix(text, "]")
}

// classifierProviderType names the wire protocol to build the stream with: the
// resolved provider's type, or the reference itself when it resolved to nothing
// and the ref is all we know.
func classifierProviderType(providerID string, creds config.ProviderCreds) string {
	if t := strings.TrimSpace(creds.Type); t != "" {
		return t
	}
	return providerID
}

func parseIntentResponse(raw string) (Intent, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return IntentNone, fmt.Errorf("empty classifier response")
	}
	if strings.HasPrefix(raw, "```") {
		raw = strings.Trim(raw, "`")
		raw = strings.TrimPrefix(raw, "json")
		raw = strings.TrimSpace(raw)
	}

	var payload struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err == nil {
		return normalizeIntent(payload.Action)
	}
	return normalizeIntent(raw)
}

func normalizeIntent(raw string) (Intent, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(IntentHelp):
		return IntentHelp, nil
	case string(IntentNew):
		return IntentNew, nil
	case string(IntentAbort):
		return IntentAbort, nil
	case string(IntentCompact):
		return IntentCompact, nil
	case string(IntentNone):
		return IntentNone, nil
	default:
		return IntentNone, fmt.Errorf("unsupported intent %q", raw)
	}
}

// IntentToCommand maps an intent onto the slash command that implements it. The
// coordinator routes only the confirmation-free intents through it: an inferred
// "start over" is never executed as a reset — only a typed `/new` is consent —
// so IntentNew never reaches a command here.
func IntentToCommand(intent Intent) string {
	switch intent {
	case IntentHelp:
		return "/help"
	case IntentNew:
		return "/new"
	case IntentAbort:
		return "/abort"
	case IntentCompact:
		return "/compact"
	default:
		return ""
	}
}
