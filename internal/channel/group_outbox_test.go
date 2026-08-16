package channel

import (
	"testing"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

func TestGroupOutboxEnvelopePreservesLifecycleFeedback(t *testing.T) {
	raw, err := EncodeGroupOutboxEnvelopeWithFeedback([]pkgchannel.Mention{{PlatformID: "bot"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := DecodeGroupOutboxEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !envelope.LifecycleFeedback || len(envelope.Mentions) != 1 || envelope.Mentions[0].PlatformID != "bot" {
		t.Fatalf("decoded envelope = %#v", envelope)
	}
}
