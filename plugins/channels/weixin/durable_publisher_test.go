package weixin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/CherryHQ/stella/pkg/channel"
)

type durableCompletionProbe struct {
	outcome channel.EgressOutcome
	done    chan struct{}
}

func (p *durableCompletionProbe) Check(context.Context) error { return nil }
func (p *durableCompletionProbe) Done() <-chan struct{}       { return p.done }
func (p *durableCompletionProbe) Ack(_ context.Context, outcome channel.EgressOutcome) error {
	p.outcome = outcome
	close(p.done)
	return nil
}

func TestDurablePublisherUnknownSendIsNotRetried(t *testing.T) {
	for _, fail := range []bool{false, true} {
		var sends atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/ilink/bot/stream/init_stream":
				_, _ = fmt.Fprint(w, `{"stream_ticket":"ticket","base_response":{"ret":0}}`)
			case "/ilink/bot/stream/sync_stream":
				sends.Add(1)
				if fail {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				_, _ = fmt.Fprint(w, `{"base_response":{"ret":0}}`)
			default:
				t.Errorf("unexpected request, including fallback: %s", r.URL.Path)
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(server.Close)
		capability := durableReplyCapability(WeixinMessage{FromUserID: "recipient", ContextToken: "secret"})
		publisher, err := NewDurablePublisher(Config{BotToken: "test", BaseURL: server.URL}, capability.Secret, capability.ExpiresAt)
		if err != nil {
			t.Fatal(err)
		}
		events := make(chan channel.Event, 1)
		events <- channel.Event{Text: "recovered reply"}
		close(events)
		probe := &durableCompletionProbe{done: make(chan struct{})}
		err = publisher.PublishIncoming(t.Context(), channel.DurablePublishRequest{
			TargetID: "recipient", Stream: &channel.ChatStream{Events: events, Completion: probe},
		})
		if (err != nil) != fail {
			t.Fatalf("fail=%v, publish error=%v", fail, err)
		}
		want := channel.EgressDelivered
		if fail {
			want = channel.EgressUnknown
		}
		if sends.Load() != 1 || probe.outcome != want {
			t.Fatalf("fail=%v sends=%d outcome=%s, want one send and %s", fail, sends.Load(), probe.outcome, want)
		}
	}
}

func TestDurablePublisherRejectsDifferentRecipientWithoutSending(t *testing.T) {
	capability := durableReplyCapability(WeixinMessage{FromUserID: "owner", ContextToken: "secret"})
	publisher, err := NewDurablePublisher(Config{BotToken: "test", BaseURL: "http://127.0.0.1:1"}, capability.Secret, capability.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan channel.Event)
	close(events)
	probe := &durableCompletionProbe{done: make(chan struct{})}
	err = publisher.PublishIncoming(t.Context(), channel.DurablePublishRequest{
		TargetID: "another-user", Stream: &channel.ChatStream{Events: events, Completion: probe},
	})
	if err == nil || probe.outcome != channel.EgressDiscarded {
		t.Fatalf("recipient mismatch: error=%v outcome=%s", err, probe.outcome)
	}
}
