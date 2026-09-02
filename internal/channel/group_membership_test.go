package channel

import (
	"testing"

	"github.com/CherryHQ/stella/internal/platform/config"
)

func TestValidateGroupChannelFailsClosed(t *testing.T) {
	valid := config.Channel{ID: "telegram-bot", Type: "telegram", AgentID: "agent-1", Enabled: true}
	tests := []struct {
		name    string
		channel config.Channel
		wantErr bool
	}{
		{name: "valid", channel: valid},
		{name: "disabled", channel: func() config.Channel { ch := valid; ch.Enabled = false; return ch }(), wantErr: true},
		{name: "wrong platform", channel: func() config.Channel { ch := valid; ch.Type = "discord"; return ch }(), wantErr: true},
		{name: "unbound", channel: func() config.Channel { ch := valid; ch.AgentID = ""; return ch }(), wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateGroupChannel(tc.channel, "telegram")
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateGroupChannel() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
