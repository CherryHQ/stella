package tracehook_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/observability/tracehook"
	"github.com/CherryHQ/stella/pkg/agent"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
	"github.com/CherryHQ/stella/pkg/providers"
)

func TestCodeChildTraceUsesNestedAuditIDAndRedactsIO(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	streamCalls := 0
	stream := func(_ context.Context, _ ai.Model, _ ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
		streamCalls++
		out := providers.NewChannelEventStream(2)
		go func() {
			if streamCalls == 1 {
				out.Emit(ai.EventToolCallDelta{ID: "outer", Name: "code", Arguments: `{"code":"return await tools.invoke('effect', { token: 'sk-proj-abcdef1234567890' });"}`})
				out.Emit(ai.EventStop{Reason: ai.StopReasonToolUse})
			} else {
				out.Emit(ai.EventStop{Reason: ai.StopReasonStop})
			}
			out.Finish(nil)
		}()
		return out, nil
	}

	runner, err := agent.NewRunner(agent.RunnerConfig{
		Stream: stream,
		Tools: agent.ToolSet{"effect": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
			return []ai.ContentBlock{ai.TextContent{Text: "token=sk-proj-abcdef1234567890"}}, nil
		}},
		ToolDefinitions: []ai.ToolDefinition{{Name: "effect"}},
	}, agent.WithToolMode(agent.ToolModeCode), agent.WithHooks(hooks.NewHookSet([]hooks.HookPlugin{tracehook.New(false, false)}), hooks.HookMeta{AgentID: "agent", SessionID: "session", UserID: "user"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RunWithActiveStart(context.Background(), []ai.Message{ai.UserMessage{Content: "go"}}, 0, nil); err != nil {
		t.Fatal(err)
	}

	got := logs.String()
	if strings.Count(got, `"call_id":"outer:1"`) != 2 || !strings.Contains(got, `"tool":"effect"`) || !strings.Contains(got, `"duration"`) {
		t.Fatalf("nested trace records = %s", got)
	}
	if strings.Contains(got, "sk-proj-abcdef1234567890") || strings.Count(got, "[REDACTED]") < 2 {
		t.Fatalf("trace IO was not redacted: %s", got)
	}
}
