package runtime

import "testing"

func BenchmarkSessionHubReplayTextTurn(b *testing.B) {
	for _, chunks := range []int{1500, 10000} {
		b.Run(testName(chunks), func(b *testing.B) {
			event := Event{Text: "0123456789"}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				hub := NewSessionHub()
				hub.begin("session")
				for range chunks {
					hub.publish("session", event)
				}
				hub.end("session")
			}
			b.ReportMetric(float64(chunks), "events/turn")
		})
	}
}

func BenchmarkSessionHubReplayAndSubscriberTextTurn(b *testing.B) {
	event := Event{Text: "0123456789"}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		hub := NewSessionHub()
		hub.begin("session")
		stream, cancel := hub.Subscribe("session")
		done := make(chan struct{})
		go func() {
			for range stream {
			}
			close(done)
		}()
		for range 1500 {
			hub.publish("session", event)
		}
		hub.end("session")
		cancel()
		<-done
	}
	b.ReportMetric(1500, "events/turn")
}

func testName(n int) string {
	if n == 1500 {
		return "1500Chunks"
	}
	return "10000Chunks"
}
