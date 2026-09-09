package channel

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestIncomingMessageJSONOmitsTransientReplyCapability(t *testing.T) {
	message := IncomingMessage{
		Platform:  PlatformDingTalk,
		ChannelID: "channel-1",
		Content:   nil,
		ReplyCapability: &ReplyCapability{
			Kind:      DingTalkSessionWebhookCapability,
			Secret:    "session-webhook-secret",
			ExpiresAt: time.Now().UTC().Add(time.Minute),
		},
	}

	raw, err := json.Marshal(message)
	if err != nil {
		t.Fatalf("marshal incoming message: %v", err)
	}
	encoded := string(raw)
	if strings.Contains(encoded, "session-webhook-secret") {
		t.Fatalf("transient reply capability secret leaked into JSON: %s", encoded)
	}
	if strings.Contains(encoded, "ReplyCapability") {
		t.Fatalf("transient reply capability field leaked into JSON: %s", encoded)
	}
}
