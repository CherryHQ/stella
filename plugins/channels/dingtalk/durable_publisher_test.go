package dingtalk

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/pkg/channel"
)

func TestDurablePublisherDoesNotExposeCapabilityInTransportError(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("untrusted test TLS certificate must fail before delivery")
	}))
	defer server.Close()
	const token = "synthetic-durable-capability"
	publisher, err := NewDurableGroupPublisher(server.URL+"/?access_token="+token, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan channel.Event, 1)
	events <- channel.Event{Text: "test reply"}
	close(events)
	err = publisher.Publish(context.Background(), channel.GroupPublishRequest{
		Platform: "dingtalk", PlatformGroupID: "group-1", Stream: &channel.ChatStream{Events: events},
	})
	if err == nil {
		t.Fatal("expected transport failure")
	}
	if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), server.URL) {
		t.Fatal("transport failure exposed the durable credential URL")
	}
}
