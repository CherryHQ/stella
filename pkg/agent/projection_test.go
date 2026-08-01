package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
)

func canonicalRef(id string) ai.ImageRefContent {
	return ai.ImageRefContent{
		MediaID:  id,
		MimeType: "image/png",
		Baseline: ai.ImageBaseline{Status: ai.ImageBaselineUnavailable},
	}
}

func TestProjectImagesMatrix(t *testing.T) {
	for _, model := range []ai.Model{supportedModel(), unsupportedModel(), unknownModel()} {
		t.Run(fmt.Sprintf("%v", model.ImageCapability()), func(t *testing.T) {
			loads := 0
			cfg := loopConfig{
				Model:           model,
				ImageProjection: true,
				ImageText: func(_ context.Context, index int, _ ai.ImageContent) string {
					return fmt.Sprintf("legacy-%d", index)
				},
				MediaLoader: func(_ context.Context, id string) (ai.ImageContent, error) {
					loads++
					return ai.ImageContent{Data: "pixels-" + id, MimeType: "image/png"}, nil
				},
			}
			messages := []ai.Message{
				ai.UserMessage{Content: []ai.ContentBlock{canonicalRef("historical-user")}},
				ai.ToolResultMessage{Content: []ai.ContentBlock{canonicalRef("historical-tool")}},
				ai.UserMessage{Content: []ai.ContentBlock{canonicalRef("active-user")}},
				ai.ToolResultMessage{Content: []ai.ContentBlock{canonicalRef("active-tool")}},
				ai.UserMessage{Content: []ai.ContentBlock{ai.ImageContent{Data: "legacy", MimeType: "image/png"}}},
			}
			out, _, err := projectImages(context.Background(), cfg, messages, 2, map[string]ai.ImageContent{})
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
			if ai.HasImage(messageBlocks(out[0])) || ai.HasImage(messageBlocks(out[1])) {
				t.Fatal("historical user/tool refs must be baseline text for every model")
			}
			wantActivePixels := model.ImageCapability() == ai.ImageSupported
			if got := ai.HasImage(messageBlocks(out[2])); got != wantActivePixels {
				t.Fatalf("active user pixels = %v, want %v", got, wantActivePixels)
			}
			if got := ai.HasImage(messageBlocks(out[3])); got != wantActivePixels {
				t.Fatalf("active tool pixels = %v, want %v", got, wantActivePixels)
			}
			if got := ai.HasImage(messageBlocks(out[4])); got != wantActivePixels {
				t.Fatalf("active legacy pixels = %v, want %v", got, wantActivePixels)
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

func TestProjectImagesRejectsAssistantImageAndProviderLeaks(t *testing.T) {
	cfg := loopConfig{Model: supportedModel(), ImageProjection: true}
	_, _, err := projectImages(context.Background(), cfg, []ai.Message{
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

func TestNextTurnProjectsPriorImageAsStableBaseline(t *testing.T) {
	ref := canonicalRef("prior-image")
	prefix := ai.UserMessage{Content: "unchanged prefix"}
	current := ai.UserMessage{Content: []ai.ContentBlock{ref}}
	cfg := loopConfig{Model: supportedModel(), ImageProjection: true, MediaLoader: func(context.Context, string) (ai.ImageContent, error) {
		return ai.ImageContent{Data: "pixels", MimeType: "image/png"}, nil
	}}
	first, _, err := projectImages(context.Background(), cfg, []ai.Message{prefix, current}, 1, map[string]ai.ImageContent{})
	if err != nil {
		t.Fatal(err)
	}
	next, _, err := projectImages(context.Background(), cfg, []ai.Message{prefix, current, ai.UserMessage{Content: "next turn"}}, 2, map[string]ai.ImageContent{})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%#v", first[0]) != fmt.Sprintf("%#v", next[0]) {
		t.Fatal("prefix before prior image changed across turns")
	}
	blocks := messageBlocks(next[1])
	if len(blocks) != 1 || ai.HasImage(blocks) {
		t.Fatalf("prior image was not reduced to baseline: %#v", blocks)
	}
	if got := blocks[0].(ai.TextContent).Text; got != imageRefProjection(ref) {
		t.Fatalf("baseline wrapper = %q, want %q", got, imageRefProjection(ref))
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
	}, Model: supportedModel()}, WithMediaLoader(func(_ context.Context, id string) (ai.ImageContent, error) {
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
	}, WithMediaLoader(func(_ context.Context, id string) (ai.ImageContent, error) {
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
	}}, WithMediaLoader(func(context.Context, string) (ai.ImageContent, error) {
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
