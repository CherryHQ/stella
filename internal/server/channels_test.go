package server

import (
	"testing"

	"github.com/CherryHQ/stella/internal/platform/config"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// Webhooks are no longer channel platforms. This keeps the general deployment
// channel default contract covered without retaining a nonexistent webhook type.
func TestChannelFromWriteRequestEnabledDefaults(t *testing.T) {
	s := &Server{}
	telegram := pkgchannel.PlatformTelegram
	enabledFalse := false
	cases := []struct {
		name              string
		req               channelWriteRequest
		existing          config.Channel
		hasExisting, want bool
	}{
		{"create bot defaults disabled", channelWriteRequest{ID: "tg", Type: &telegram}, config.Channel{}, false, false},
		{"create bot honors explicit disabled", channelWriteRequest{ID: "tg", Type: &telegram, Enabled: &enabledFalse}, config.Channel{}, false, false},
		{"update preserves disabled", channelWriteRequest{ID: "tg", Type: &telegram}, config.Channel{ID: "tg", Type: telegram, Enabled: false}, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.channelFromWriteRequest(nil, tc.req, tc.existing, tc.hasExisting).Enabled; got != tc.want {
				t.Fatalf("Enabled=%v want %v", got, tc.want)
			}
		})
	}
}
