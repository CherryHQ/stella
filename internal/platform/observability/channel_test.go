package observability

import "testing"

func TestChannelNamePreservesCanonicalBusinessNamesAtTelemetryEdge(t *testing.T) {
	for canonical, want := range map[string]string{"weixin": "wechat", "telegram": "telegram", "": ""} {
		if got := ChannelName(canonical); got != want {
			t.Fatalf("ChannelName(%q) = %q, want %q", canonical, got, want)
		}
	}
}
