package agent

import (
	"context"
	"reflect"
	"testing"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
)

// TestActiveTurnProjectionMeasurement records the provider-visible request
// shape. Canonical persistence is asserted separately by
// lcm.TestAppendStoresBaselineProjectionAndParts.
func TestActiveTurnProjectionMeasurement(t *testing.T) {
	var contexts []ai.Context
	var stats []ProjectionStats
	runner, err := NewRunner(RunnerConfig{
		Model: supportedModel(),
		Stream: func(_ context.Context, _ ai.Model, aiCtx ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
			contexts = append(contexts, aiCtx)
			out := providers.NewChannelEventStream(2)
			go func() {
				out.Emit(ai.EventStop{Reason: ai.StopReasonStop})
				out.Finish(nil)
			}()
			return out, nil
		},
	}, withTestCanonicalImages(func(_ context.Context, id string) (ai.ImageContent, error) {
		return ai.ImageContent{Data: "pixels-" + id, MimeType: "image/png"}, nil
	}), WithProjectionObserver(func(observation ProjectionStats) {
		stats = append(stats, observation)
	}))
	if err != nil {
		t.Fatal(err)
	}

	prefix := ai.UserMessage{Content: "stable prefix"}
	image := ai.UserMessage{Content: []ai.ContentBlock{canonicalReadyRef("image-1")}}
	if _, err := runner.RunWithActiveStart(context.Background(), []ai.Message{prefix, image}, 1, nil); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if _, err := runner.RunWithActiveStart(context.Background(), []ai.Message{prefix, image, ai.UserMessage{Content: "next turn"}}, 2, nil); err != nil {
		t.Fatalf("next turn: %v", err)
	}

	if len(contexts) != 2 || len(stats) != 2 {
		t.Fatalf("provider contexts/stats = %d/%d, want 2/2", len(contexts), len(stats))
	}
	firstImages := providerImageCount(contexts[0].Messages)
	nextImages := providerImageCount(contexts[1].Messages)
	if firstImages != 1 || nextImages != 0 {
		t.Fatalf("provider image count first/next = %d/%d, want 1/0", firstImages, nextImages)
	}
	if stats[0].Hydrations != 1 || stats[1].Hydrations != 0 {
		t.Fatalf("hydrations first/next = %d/%d, want 1/0", stats[0].Hydrations, stats[1].Hydrations)
	}
	if stats[1].BaselineProjections != 1 {
		t.Fatalf("next-turn baseline projections = %d, want 1", stats[1].BaselineProjections)
	}

	firstDiff := firstDifferingMessage(contexts[0].Messages, contexts[1].Messages)
	if firstDiff != 1 {
		t.Fatalf("first provider-message diff = %d, want image message 1", firstDiff)
	}
	if !reflect.DeepEqual(contexts[0].Messages[:firstDiff], contexts[1].Messages[:firstDiff]) {
		t.Fatal("provider prefix before image changed")
	}
	t.Logf("active-turn projection: provider_images=%d→%d hydrations=%d→%d first_diff=%d canonical_base64=covered-by-lcm-canonical-append", firstImages, nextImages, stats[0].Hydrations, stats[1].Hydrations, firstDiff)
}

func providerImageCount(messages []ai.Message) int {
	count := 0
	for _, message := range messages {
		for _, block := range messageBlocks(message) {
			if _, ok := block.(ai.ImageContent); ok {
				count++
			}
		}
	}
	return count
}

func firstDifferingMessage(left, right []ai.Message) int {
	for i := range min(len(left), len(right)) {
		if !reflect.DeepEqual(left[i], right[i]) {
			return i
		}
	}
	if len(left) != len(right) {
		return min(len(left), len(right))
	}
	return -1
}
