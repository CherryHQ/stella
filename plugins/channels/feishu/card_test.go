package feishu

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/pkg/channel"
)

func TestBuildCardContentIncludesStablePresentation(t *testing.T) {
	content, err := buildCardContentForStatus("hello **world**", cardStatusRunning)
	if err != nil {
		t.Fatalf("build card: %v", err)
	}
	var card map[string]any
	if err := json.Unmarshal([]byte(content), &card); err != nil {
		t.Fatalf("decode card: %v", err)
	}
	config := card["config"].(map[string]any)
	if config["update_multi"] != true {
		t.Fatalf("update_multi = %#v, want true", config["update_multi"])
	}
	summary := config["summary"].(map[string]any)["content"]
	if summary != "hello **world**" {
		t.Fatalf("summary = %#v", summary)
	}
	header := card["header"].(map[string]any)
	if header["template"] != "blue" {
		t.Fatalf("header template = %#v, want blue", header["template"])
	}
	body := card["body"].(map[string]any)
	elements := body["elements"].([]any)
	if elements[0].(map[string]any)["element_id"] != "content_0" {
		t.Fatalf("first element has no stable id: %#v", elements[0])
	}
}

func TestValidateCardLimitsRejectsElementOverflow(t *testing.T) {
	elements := make([]any, maxFeishuCardElements+1)
	for i := range elements {
		elements[i] = map[string]any{"tag": "markdown"}
	}
	card := map[string]any{"body": map[string]any{"elements": elements}}
	if err := validateCardLimits(card, 1); err == nil {
		t.Fatal("expected element limit error")
	}
}

func TestSplitCardTextPreservesCodeFences(t *testing.T) {
	body := strings.Repeat("0123456789\n", 500)
	chunks := splitCardText("```go\n"+body+"```", cardStatusCompleted)
	if len(chunks) < 2 {
		t.Fatalf("chunks = %d, want multiple", len(chunks))
	}
	for i, chunk := range chunks {
		if !strings.HasPrefix(chunk, "```go\n") || !strings.HasSuffix(chunk, "\n```") {
			t.Fatalf("chunk %d broke code fence: %q", i, chunk[:min(len(chunk), 40)])
		}
	}
}

func TestSplitCardTextRepeatsTableHeader(t *testing.T) {
	row := "| value | " + strings.Repeat("x", 80) + " |\n"
	table := "| name | detail |\n| --- | --- |\n" + strings.Repeat(row, 100)
	chunks := splitCardText(strings.TrimSuffix(table, "\n"), cardStatusCompleted)
	if len(chunks) < 2 {
		t.Fatalf("chunks = %d, want multiple", len(chunks))
	}
	for i, chunk := range chunks {
		if !strings.HasPrefix(chunk, "| name | detail |\n| --- | --- |") {
			t.Fatalf("chunk %d lost table header", i)
		}
	}
}

func TestMarkdownBlocksPreservesDetailsPanel(t *testing.T) {
	panel := "<details>\n<summary>timeline</summary>\n\nfirst\n\nsecond\n\n</details>"
	blocks := markdownBlocks("answer\n\n" + panel)
	if len(blocks) != 2 || blocks[1] != panel {
		t.Fatalf("blocks = %#v, want intact details panel", blocks)
	}
}

func TestSplitCardTextPreservesOversizeDetailsPanel(t *testing.T) {
	panel := "<details open>\n<summary>timeline</summary>\n\n" + strings.Repeat("long reasoning\n", 500) + "\n</details>"
	chunks := splitCardText(panel, cardStatusCompleted)
	if len(chunks) < 2 {
		t.Fatalf("chunks = %d, want multiple", len(chunks))
	}
	for i, chunk := range chunks {
		if !strings.HasPrefix(chunk, "<details open>\n<summary>timeline</summary>") || !strings.HasSuffix(chunk, "</details>") {
			t.Fatalf("chunk %d broke details panel: %q", i, chunk[:min(len(chunk), 80)])
		}
	}
}

func TestStreamTimelinePreservesOrderAndRedactsToolInput(t *testing.T) {
	timeline := &streamTimeline{}
	timeline.addReasoning("先检查。")
	timeline.handleTool(&channel.ToolUseEvent{ID: "1", Tool: "bash", Status: "running", Input: "token=secret-value run tests"})
	timeline.handleTool(&channel.ToolUseEvent{ID: "1", Tool: "bash", Status: "done", Detail: "must-not-appear"})
	timeline.addReasoning("再总结。")

	got := timeline.markdown(false)
	firstReasoning := strings.Index(got, "先检查")
	tool := strings.Index(got, "bash")
	lastReasoning := strings.Index(got, "再总结")
	if firstReasoning < 0 || tool <= firstReasoning || lastReasoning <= tool {
		t.Fatalf("timeline order is wrong: %q", got)
	}
	if strings.Contains(got, "secret-value") || strings.Contains(got, "must-not-appear") {
		t.Fatalf("timeline leaked raw tool data: %q", got)
	}
	if !strings.Contains(got, "token=[REDACTED]") {
		t.Fatalf("timeline did not show redacted input: %q", got)
	}
}

func TestStreamTimelineSplitsLongReasoningIntoValidPanels(t *testing.T) {
	timeline := &streamTimeline{}
	timeline.addReasoning(strings.Repeat("思考内容。", 1_000))
	markdown := timeline.markdown(false)
	if panels := strings.Count(markdown, "<details>"); panels < 2 {
		t.Fatalf("panels = %d, want multiple", panels)
	}
	for _, block := range markdownBlocks(markdown) {
		if len(block) > maxTimelinePanelBytes+256 {
			t.Fatalf("timeline panel is too large: %d bytes", len(block))
		}
	}
}

func TestStableDeliveryUUID(t *testing.T) {
	first := stableDeliveryUUID("bot", "chat", "message", "turn")
	if len(first) != 50 {
		t.Fatalf("uuid length = %d, want 50", len(first))
	}
	if first != stableDeliveryUUID("bot", "chat", "message", "turn") {
		t.Fatal("same delivery produced a different uuid")
	}
	if first == stableDeliveryUUID("bot", "chat", "message", "other-turn") {
		t.Fatal("different delivery produced the same uuid")
	}
}

func TestReplyMessageBodyIncludesDeliveryUUID(t *testing.T) {
	body := replyMessageBodyWithUUID("interactive", `{}`, true, "stable-id")
	if body.Uuid == nil || *body.Uuid != "stable-id" {
		t.Fatalf("uuid = %#v, want stable-id", body.Uuid)
	}
}

func TestStreamDrainsLatestSnapshot(t *testing.T) {
	patches := make([]string, 0, 1)
	bot := &Bot{
		replyCardFn: func(_ context.Context, _, _ string) (string, error) {
			return "om_progress", nil
		},
		patchCardFn: func(_ context.Context, _ string, content string) error {
			patches = append(patches, content)
			return nil
		},
	}
	events := make(chan channel.Event, 2)
	events <- channel.Event{Text: "hello "}
	events <- channel.Event{Text: "world"}
	close(events)

	_, response, _, _, _, _, err := bot.streamResponseInThread(t.Context(), events, "oc_chat", "om_request", "", "turn-1")
	if err != nil {
		t.Fatalf("stream response: %v", err)
	}
	if response != "hello world" {
		t.Fatalf("response = %q", response)
	}
	if len(patches) != 1 || !strings.Contains(patches[0], "hello world") {
		t.Fatalf("patches = %#v, want one drained latest snapshot", patches)
	}
}

func TestStreamReturnsCompleteReasoningAndToolTimeline(t *testing.T) {
	bot := &Bot{
		replyCardFn: func(_ context.Context, _, _ string) (string, error) {
			return "om_progress", nil
		},
		patchCardFn: func(_ context.Context, _ string, _ string) error { return nil },
	}
	events := make(chan channel.Event, 4)
	events <- channel.Event{Reasoning: "inspect"}
	events <- channel.Event{ToolUse: &channel.ToolUseEvent{ID: "1", Tool: "read", Status: "running", Input: "config"}}
	events <- channel.Event{ToolUse: &channel.ToolUseEvent{ID: "1", Tool: "read", Status: "done"}}
	events <- channel.Event{Text: "answer"}
	close(events)

	_, response, _, _, _, _, err := bot.streamResponseInThread(t.Context(), events, "oc_chat", "om_request", "", "turn-1")
	if err != nil {
		t.Fatalf("stream response: %v", err)
	}
	for _, want := range []string{"answer", "<details>", "inspect", "read"} {
		if !strings.Contains(response, want) {
			t.Fatalf("response missing %q: %q", want, response)
		}
	}
}
