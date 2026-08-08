package access

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/agent"
)

func BenchmarkEventDelivery1500Chunks(b *testing.B) {
	b.Run("Direct", func(b *testing.B) {
		benchmarkEventDelivery(b, func(_ context.Context, source <-chan agent.Event) <-chan agent.Event {
			return source
		})
	})
	b.Run("RelayWithStallTimer", func(b *testing.B) {
		benchmarkEventDelivery(b, relayEventsUntilDone)
	})
}

func benchmarkEventDelivery(b *testing.B, wrap func(context.Context, <-chan agent.Event) <-chan agent.Event) {
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		source := make(chan agent.Event, 100)
		output := wrap(context.Background(), source)
		go func() {
			for range 1500 {
				source <- agent.Event{Text: "0123456789"}
			}
			close(source)
		}()
		count := 0
		for range output {
			count++
		}
		if count != 1500 {
			b.Fatalf("events = %d, want 1500", count)
		}
	}
	b.ReportMetric(1500, "events/turn")
}
