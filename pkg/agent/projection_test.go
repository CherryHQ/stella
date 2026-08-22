package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
)

func supportedModel() ai.Model   { return ai.Model{Input: []string{"text", "image"}} }
func unsupportedModel() ai.Model { return ai.Model{Input: []string{"text"}} }
func unknownModel() ai.Model     { return ai.Model{} }

func canonicalRef(id string) ai.ImageRefContent {
	return ai.ImageRefContent{MediaID: id}
}

func canonicalReadyRef(id string) ai.ImageRefContent {
	return ai.ImageRefContent{
		MediaID:  id,
		Baseline: ai.ImageBaseline{Text: "## Text\ninvoice total: $42\n\n## Scene\nA scanned invoice."},
	}
}

func testCanonicalConfig(loader MediaLoader) *CanonicalImageConfig {
	return &CanonicalImageConfig{
		Load: loader,
		CanonicalizeToolResult: func(_ context.Context, result ai.ToolResultMessage) (ai.ToolResultMessage, error) {
			return result, nil
		},
	}
}

func withTestCanonicalImages(loader MediaLoader) Option {
	return WithCanonicalImages(*testCanonicalConfig(loader))
}

func TestProjectImagesMatrix(t *testing.T) {
	for _, model := range []ai.Model{supportedModel(), unsupportedModel(), unknownModel()} {
		t.Run(fmt.Sprintf("%v", model.ImageCapability()), func(t *testing.T) {
			loads := 0
			cfg := loopConfig{
				Model: model,
				CanonicalImages: testCanonicalConfig(func(_ context.Context, id string) (ai.ImageContent, error) {
					loads++
					return ai.ImageContent{Data: "pixels-" + id, MimeType: "image/png"}, nil
				}),
			}
			messages := []ai.Message{
				ai.UserMessage{Content: []ai.ContentBlock{canonicalRef("historical-user")}},
				ai.ToolResultMessage{Content: []ai.ContentBlock{canonicalRef("historical-tool")}},
				ai.UserMessage{Content: []ai.ContentBlock{ai.ImageContent{Data: "historical-inline", MimeType: "image/png"}}},
				ai.UserMessage{Content: []ai.ContentBlock{canonicalRef("active-user")}},
				ai.ToolResultMessage{Content: []ai.ContentBlock{canonicalRef("active-tool")}},
				ai.UserMessage{Content: []ai.ContentBlock{ai.ImageContent{Data: "active-inline", MimeType: "image/png"}}},
			}
			out, err := projectImages(context.Background(), cfg, messages, 3, map[string]ai.ImageContent{})
			if err != nil {
				t.Fatalf("project: %v", err)
			}
			for i, msg := range out {
				for _, block := range messageBlocks(msg) {
					if _, ok := block.(ai.ImageRefContent); ok {
						t.Fatalf("message %d leaked ImageRefContent", i)
					}
				}
			}
			if ai.HasImage(messageBlocks(out[0])) || ai.HasImage(messageBlocks(out[1])) || ai.HasImage(messageBlocks(out[2])) {
				t.Fatal("historical canonical and inline images must be text for every model")
			}
			wantActivePixels := model.ImageCapability() == ai.ImageSupported
			if got := ai.HasImage(messageBlocks(out[3])); got != wantActivePixels {
				t.Fatalf("active user pixels = %v, want %v", got, wantActivePixels)
			}
			if got := ai.HasImage(messageBlocks(out[4])); got != wantActivePixels {
				t.Fatalf("active tool pixels = %v, want %v", got, wantActivePixels)
			}
			if got := ai.HasImage(messageBlocks(out[5])); got != wantActivePixels {
				t.Fatalf("active inline pixels = %v, want %v", got, wantActivePixels)
			}
			if wantActivePixels && loads != 2 {
				t.Fatalf("loads = %d, want active refs only", loads)
			}
			if !wantActivePixels && loads != 0 {
				t.Fatalf("loads = %d, want no unsupported/unknown hydration", loads)
			}
		})
	}
}

func TestProjectedToolBudgetCountsFlattenTextSeparators(t *testing.T) {
	t.Setenv("STELLA_TOOL_MAX_TURN_BYTES", "301")
	block := strings.Repeat("x", 100)
	messages := []ai.Message{ai.ToolResultMessage{Content: []ai.ContentBlock{
		ai.TextContent{Text: block},
		ai.TextContent{Text: block},
		ai.TextContent{Text: block},
	}}}

	projected, err := projectImages(context.Background(), loopConfig{Model: unsupportedModel()}, messages, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	result := projected[0].(ai.ToolResultMessage)
	visible := ai.FlattenText(result.Content)
	if len(visible) > 301 {
		t.Fatalf("provider-visible result is %d bytes, exceeds budget 301", len(visible))
	}
	if !strings.Contains(visible, "turn's output budget was exhausted") {
		t.Fatalf("separator bytes did not trigger truncation: %q", visible)
	}
}

func TestProjectedToolBudgetCountsImageBaselines(t *testing.T) {
	t.Setenv("STELLA_TOOL_MAX_TURN_BYTES", "600")
	baseline := ai.ImageBaseline{Text: "## Text\n" + strings.Repeat("ocr", 180) + "\n\n## Scene\n" + strings.Repeat("scene", 20)}
	messages := []ai.Message{
		ai.ToolResultMessage{ToolCallID: "1", Content: []ai.ContentBlock{ai.ImageRefContent{MediaID: "1", Baseline: baseline}}},
		ai.ToolResultMessage{ToolCallID: "2", Content: []ai.ContentBlock{ai.ImageRefContent{MediaID: "2", Baseline: baseline}}},
	}

	projected, err := projectImages(context.Background(), loopConfig{Model: unsupportedModel()}, messages, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for i, message := range projected {
		visible := ai.FlattenText(message.(ai.ToolResultMessage).Content)
		total += len(visible)
		if !strings.Contains(visible, "turn's output budget was exhausted") {
			t.Fatalf("result %d baseline was not budgeted: %q", i, visible)
		}
	}
	if total > 600 {
		t.Fatalf("provider-visible baselines total %d bytes, exceeds budget 600", total)
	}
}

func TestProjectedToolBudgetPreservesMixedBlockOrder(t *testing.T) {
	t.Setenv("STELLA_TOOL_MAX_TURN_BYTES", "600")
	messages := []ai.Message{ai.ToolResultMessage{Content: []ai.ContentBlock{
		ai.TextContent{Text: "A" + strings.Repeat("a", 499)},
		ai.ImageRefContent{MediaID: "image", Baseline: ai.ImageBaseline{Text: "## Text\nimage\n\n## Scene\nchart"}},
		ai.TextContent{Text: "B" + strings.Repeat("b", 499)},
	}}}
	cfg := loopConfig{Model: supportedModel(), CanonicalImages: testCanonicalConfig(func(context.Context, string) (ai.ImageContent, error) {
		return ai.ImageContent{Data: "pixels", MimeType: "image/png"}, nil
	})}

	projected, err := projectImages(context.Background(), cfg, messages, 0, map[string]ai.ImageContent{})
	if err != nil {
		t.Fatal(err)
	}
	blocks := projected[0].(ai.ToolResultMessage).Content
	if len(blocks) != 3 {
		t.Fatalf("blocks = %#v, want three blocks", blocks)
	}
	first, firstOK := blocks[0].(ai.TextContent)
	_, imageOK := blocks[1].(ai.ImageContent)
	last, lastOK := blocks[2].(ai.TextContent)
	if !firstOK || !imageOK || !lastOK {
		t.Fatalf("block order changed: %#v", blocks)
	}
	if !strings.HasPrefix(first.Text, "A") || !strings.Contains(first.Text, "turn's output budget was exhausted") {
		t.Fatalf("head text or marker missing before image: %q", first.Text)
	}
	if last.Text == "" || strings.Trim(last.Text, "b") != "" {
		t.Fatalf("tail text moved before image: %q", last.Text)
	}
	if len(ai.FlattenText(blocks)) > 600 {
		t.Fatalf("provider-visible mixed result exceeds budget: %d", len(ai.FlattenText(blocks)))
	}
}

func TestProjectImagesRejectsAssistantImageAndProviderLeaks(t *testing.T) {
	cfg := loopConfig{Model: supportedModel()}
	_, err := projectImages(context.Background(), cfg, []ai.Message{
		ai.AssistantMessage{Content: []ai.ContentBlock{canonicalRef("bad")}},
	}, 0, nil)
	if !errors.Is(err, ErrAssistantImageBlock) {
		t.Fatalf("assistant ref error = %v, want %v", err, ErrAssistantImageBlock)
	}
	if err := validateProviderImages(unsupportedModel(), []ai.Message{ai.UserMessage{Content: []ai.ContentBlock{ai.ImageContent{Data: "x"}}}}); !errors.Is(err, ErrUnsupportedImage) {
		t.Fatalf("unsupported validation = %v", err)
	}
	if err := validateProviderImages(supportedModel(), []ai.Message{ai.UserMessage{Content: []ai.ContentBlock{canonicalRef("bad")}}}); !errors.Is(err, ErrImageRefUnresolved) {
		t.Fatalf("ref validation = %v", err)
	}
}

func TestActiveHydrationFailureUsesStoredBaseline(t *testing.T) {
	ref := canonicalReadyRef("missing")
	out, err := projectImages(context.Background(), loopConfig{Model: supportedModel(), CanonicalImages: testCanonicalConfig(func(context.Context, string) (ai.ImageContent, error) {
		return ai.ImageContent{}, errors.New("missing")
	})}, []ai.Message{ai.UserMessage{Content: []ai.ContentBlock{ref}}}, 0, map[string]ai.ImageContent{})
	if err != nil || ai.HasImage(messageBlocks(out[0])) {
		t.Fatalf("fallback = %#v err=%v", out, err)
	}
}

func TestNextTurnProjectsPriorImageAsStableBaseline(t *testing.T) {
	ref := canonicalReadyRef("prior-image")
	prefix := ai.UserMessage{Content: "unchanged prefix"}
	current := ai.UserMessage{Content: []ai.ContentBlock{ref}}
	cfg := loopConfig{Model: supportedModel(), CanonicalImages: testCanonicalConfig(func(context.Context, string) (ai.ImageContent, error) {
		return ai.ImageContent{Data: "pixels", MimeType: "image/png"}, nil
	})}
	first, err := projectImages(context.Background(), cfg, []ai.Message{prefix, current}, 1, map[string]ai.ImageContent{})
	if err != nil {
		t.Fatal(err)
	}
	next, err := projectImages(context.Background(), cfg, []ai.Message{prefix, current, ai.UserMessage{Content: "next turn"}}, 2, map[string]ai.ImageContent{})
	if err != nil {
		t.Fatal(err)
	}
	if got := first[0].(ai.UserMessage).Content; got != "unchanged prefix" {
		t.Fatalf("first prefix = %#v, want unchanged prefix", got)
	}
	if got := next[0].(ai.UserMessage).Content; got != "unchanged prefix" {
		t.Fatalf("next prefix = %#v, want unchanged prefix", got)
	}
	blocks := messageBlocks(next[1])
	if len(blocks) != 1 || ai.HasImage(blocks) {
		t.Fatalf("prior image was not reduced to baseline: %#v", blocks)
	}
	wantBaseline := "## Text\ninvoice total: $42\n\n## Scene\nA scanned invoice."
	if got := blocks[0].(ai.TextContent).Text; got != wantBaseline {
		t.Fatalf("baseline projection = %q, want stored baseline %q", got, wantBaseline)
	}
}

func TestContinueProjectsOnlyTailImageAsActive(t *testing.T) {
	var seen ai.Context
	r, err := NewRunner(RunnerConfig{Stream: func(_ context.Context, _ ai.Model, aiCtx ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
		seen = aiCtx
		out := providers.NewChannelEventStream(2)
		go func() {
			out.Emit(ai.EventStop{Reason: ai.StopReasonStop})
			out.Finish(nil)
		}()
		return out, nil
	}, Model: supportedModel()}, withTestCanonicalImages(func(_ context.Context, id string) (ai.ImageContent, error) {
		return ai.ImageContent{Data: "pixels-" + id, MimeType: "image/png"}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Continue(context.Background(), []ai.Message{
		ai.UserMessage{Content: []ai.ContentBlock{canonicalRef("historical")}},
		ai.ToolResultMessage{ToolCallID: "tail", Content: []ai.ContentBlock{canonicalRef("tail")}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ai.HasImage(messageBlocks(seen.Messages[0])) {
		t.Fatal("historical ref became active during Continue")
	}
	if !ai.HasImage(messageBlocks(seen.Messages[1])) {
		t.Fatal("continuation tail ref was not projected as active pixels")
	}
}

func TestRunWithActiveStartReusesHydrationAcrossToolLoop(t *testing.T) {
	loads := 0
	var seen []ai.Context
	calls := 0
	stream := func(_ context.Context, _ ai.Model, aiCtx ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
		seen = append(seen, aiCtx)
		calls++
		out := providers.NewChannelEventStream(4)
		go func() {
			if calls == 1 {
				out.Emit(ai.EventToolCallDelta{ID: "tool", Name: "image"})
			} else {
				out.Emit(ai.EventTextDelta{Text: "done"})
			}
			out.Emit(ai.EventStop{Reason: ai.StopReasonStop})
			out.Finish(nil)
		}()
		return out, nil
	}
	r, err := NewRunner(RunnerConfig{
		Stream: stream,
		Model:  supportedModel(),
		Tools: ToolSet{"image": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
			return []ai.ContentBlock{canonicalRef("same")}, nil
		}},
	}, withTestCanonicalImages(func(_ context.Context, id string) (ai.ImageContent, error) {
		loads++
		return ai.ImageContent{Data: "identical-pixels", MimeType: "image/png"}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.RunWithActiveStart(context.Background(), []ai.Message{
		ai.UserMessage{Content: "old"},
		ai.UserMessage{Content: []ai.ContentBlock{canonicalRef("same")}},
	}, 1, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if loads != 1 {
		t.Fatalf("loads = %d, want one per Run", loads)
	}
	if len(seen) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(seen))
	}
	first := messageBlocks(seen[0].Messages[1])[0].(ai.ImageContent)
	second := messageBlocks(seen[1].Messages[1])[0].(ai.ImageContent)
	if first != second {
		t.Fatalf("active user image changed across calls: %#v != %#v", first, second)
	}
	if !ai.HasImage(messageBlocks(seen[1].Messages[len(seen[1].Messages)-1])) {
		t.Fatal("active tool result must remain pixels")
	}
}

func TestProgressNudgesDoNotDemoteActiveImage(t *testing.T) {
	calls := 0
	milestones := make(map[int]ai.Context)
	stream := func(_ context.Context, _ ai.Model, aiCtx ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
		calls++
		if calls == 50 || calls == 80 || calls == 100 {
			milestones[calls] = aiCtx
		}
		out := providers.NewChannelEventStream(4)
		go func() {
			if calls < 100 {
				out.Emit(ai.EventToolCallDelta{ID: fmt.Sprintf("t-%d", calls), Name: "again"})
			} else {
				out.Emit(ai.EventTextDelta{Text: "done"})
			}
			out.Emit(ai.EventStop{Reason: ai.StopReasonStop})
			out.Finish(nil)
		}()
		return out, nil
	}
	r, err := NewRunner(RunnerConfig{Stream: stream, Model: supportedModel(), Tools: ToolSet{
		"again": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
			return []ai.ContentBlock{ai.TextContent{Text: "ok"}}, nil
		},
	}}, withTestCanonicalImages(func(context.Context, string) (ai.ImageContent, error) {
		return ai.ImageContent{Data: "pixels", MimeType: "image/png"}, nil
	}), WithTurnNotify(func(turn int, _ time.Duration) *string {
		switch turn {
		case 50, 80, 100:
			msg := "progress"
			return &msg
		default:
			return nil
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.RunWithActiveStart(context.Background(), []ai.Message{ai.UserMessage{Content: []ai.ContentBlock{canonicalRef("active")}}}, 0, nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	if calls != 100 {
		t.Fatalf("provider calls = %d, want 100", calls)
	}
	for _, turn := range []int{50, 80, 100} {
		ctx, ok := milestones[turn]
		if !ok || !ai.HasImage(messageBlocks(ctx.Messages[0])) {
			t.Fatalf("turn %d demoted active image: %#v", turn, ctx.Messages)
		}
	}
}
