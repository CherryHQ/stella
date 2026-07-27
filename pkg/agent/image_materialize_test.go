package agent

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
)

func stubImageText() (ImageTextFunc, *int) {
	calls := 0
	return func(_ context.Context, index int, img ai.ImageContent) string {
		calls++
		return fmt.Sprintf("rendered image %d (%s)", index, img.MimeType)
	}, &calls
}

func imageHistory() []ai.Message {
	return []ai.Message{
		ai.UserMessage{Content: []ai.ContentBlock{
			ai.TextContent{Text: "look at this"},
			ai.ImageContent{Data: "AAAA", MimeType: "image/png"},
		}},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.ToolCall{ID: "t1", Name: "read"}}},
		ai.ToolResultMessage{ToolCallID: "t1", ToolName: "read", Content: []ai.ContentBlock{
			ai.TextContent{Text: "Read image file"},
			ai.ImageContent{Data: "BBBB", MimeType: "image/jpeg"},
		}},
		ai.UserMessage{Content: "plain string content"},
	}
}

func unsupportedModel() ai.Model { return ai.Model{Input: []string{"text"}} }
func supportedModel() ai.Model   { return ai.Model{Input: []string{"text", "image"}} }
func unknownModel() ai.Model     { return ai.Model{} }

func TestMaterializeImagesReplacesImagesForUnsupportedModel(t *testing.T) {
	render, calls := stubImageText()
	history := imageHistory()

	out := materializeImages(context.Background(), loopConfig{Model: unsupportedModel(), ImageText: render}, history)

	if *calls != 2 {
		t.Fatalf("render calls = %d, want 2", *calls)
	}
	for i, msg := range out {
		if blocks := contentBlocks(msg); ai.HasImage(blocks) {
			t.Fatalf("message %d still carries an image block", i)
		}
	}
	// Images are numbered across the whole request, not per message.
	if got := ai.FlattenText(contentBlocks(out[0])); !strings.Contains(got, "rendered image 1 (image/png)") {
		t.Errorf("user message = %q, want the first rendering", got)
	}
	if got := ai.FlattenText(contentBlocks(out[2])); !strings.Contains(got, "rendered image 2 (image/jpeg)") {
		t.Errorf("tool result = %q, want the second rendering", got)
	}
	// Untouched messages are the same values, not copies with lost fields.
	if !reflect.DeepEqual(out[1], history[1]) || !reflect.DeepEqual(out[3], history[3]) {
		t.Error("messages without images must pass through unchanged")
	}
}

func TestMaterializeImagesLeavesHistoryUnmodified(t *testing.T) {
	render, _ := stubImageText()
	history := imageHistory()
	userBlocks := history[0].(ai.UserMessage).Content.([]ai.ContentBlock)
	toolBlocks := history[2].(ai.ToolResultMessage).Content

	out := materializeImages(context.Background(), loopConfig{Model: unsupportedModel(), ImageText: render}, history)

	if &out[0] == &history[0] {
		t.Error("the message slice must be copied before rewriting")
	}
	// The persisted transcript is shared with the session store: it must still
	// hold the original images so a later vision-capable model can see them.
	if !ai.HasImage(userBlocks) {
		t.Error("user content blocks were modified in place")
	}
	if !ai.HasImage(toolBlocks) {
		t.Error("tool result content blocks were modified in place")
	}
	if _, ok := userBlocks[1].(ai.ImageContent); !ok {
		t.Errorf("user image block was replaced in place: %T", userBlocks[1])
	}
}

// Only a declared "image" model keeps the image itself.
func TestMaterializeImagesSkipsDeclaredCapableModel(t *testing.T) {
	render, calls := stubImageText()
	history := imageHistory()

	out := materializeImages(context.Background(), loopConfig{Model: supportedModel(), ImageText: render}, history)

	if *calls != 0 {
		t.Errorf("render calls = %d, want 0", *calls)
	}
	if !ai.HasImage(contentBlocks(out[0])) {
		t.Error("image block must survive for a model that declared image input")
	}
}

// An undeclared model is rendered like a text-only one: providers do not report
// modalities, so undeclared is the common case, and guessing "can see" leaves
// the model staring at a placeholder.
func TestMaterializeImagesRendersForUndeclaredModel(t *testing.T) {
	render, calls := stubImageText()
	history := imageHistory()

	out := materializeImages(context.Background(), loopConfig{Model: unknownModel(), ImageText: render}, history)

	if *calls != 2 {
		t.Errorf("render calls = %d, want 2", *calls)
	}
	if ai.HasImage(contentBlocks(out[0])) {
		t.Error("an undeclared model must not receive the image itself")
	}
}

func TestMaterializeImagesNoopWithoutRenderer(t *testing.T) {
	history := imageHistory()
	out := materializeImages(context.Background(), loopConfig{Model: unsupportedModel()}, history)
	if !ai.HasImage(contentBlocks(out[0])) {
		t.Error("without a renderer, images must be sent as-is")
	}
}

// TestRunMaterializesImagesForRequestOnly checks the loop seam end to end: the
// provider sees text, the returned history still holds the image.
func TestRunMaterializesImagesForRequestOnly(t *testing.T) {
	var seen ai.Context
	stream := func(_ context.Context, _ ai.Model, aiCtx ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
		seen = aiCtx
		out := providers.NewChannelEventStream(4)
		go func() {
			out.Emit(ai.EventTextDelta{Text: "ok"})
			out.Emit(ai.EventStop{Reason: ai.StopReasonStop})
			out.Finish(nil)
		}()
		return out, nil
	}
	render, _ := stubImageText()
	runner, err := NewRunner(RunnerConfig{Stream: stream, Model: unsupportedModel()}, WithImageText(render))
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	input := []ai.Message{ai.UserMessage{Content: []ai.ContentBlock{
		ai.ImageContent{Data: "AAAA", MimeType: "image/png"},
	}}}
	history, err := runner.Run(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if ai.HasImage(contentBlocks(seen.Messages[0])) {
		t.Error("the provider must not receive an image block from a text-only model")
	}
	if !strings.Contains(ai.FlattenText(contentBlocks(seen.Messages[0])), "rendered image 1") {
		t.Error("the provider must receive the rendering instead")
	}
	if !ai.HasImage(contentBlocks(history[0])) {
		t.Error("the returned history must keep the original image")
	}
}

func contentBlocks(msg ai.Message) []ai.ContentBlock {
	switch m := msg.(type) {
	case ai.UserMessage:
		if blocks, ok := m.Content.([]ai.ContentBlock); ok {
			return blocks
		}
		return nil
	case ai.ToolResultMessage:
		return m.Content
	case ai.AssistantMessage:
		return m.Content
	default:
		return nil
	}
}
