package runtime

import (
	"testing"

	"github.com/CherryHQ/stella/internal/agent/session"
)

func TestForegroundHumanSessionClassification(t *testing.T) {
	base := session.Info{ID: "s", UserID: "u1", AgentID: "stella", Kind: string(session.KindChat)}
	cases := []struct {
		name string
		info session.Info
		want bool
	}{
		{"chat", base, true},
		{"main", func() session.Info { info := base; info.Kind = string(session.KindMain); return info }(), true},
		{"scheduler", func() session.Info { info := base; info.Kind = string(session.KindScheduler); return info }(), false},
		{"task", func() session.Info { info := base; info.Kind = string(session.KindTask); return info }(), false},
		{"delegate", func() session.Info { info := base; info.Kind = string(session.KindDelegate); return info }(), false},
		{"webhook", func() session.Info { info := base; info.Channel = string(session.ChannelWebhook); return info }(), false},
		{"group", func() session.Info { info := base; info.GroupID = "group"; return info }(), false},
		{"guest", func() session.Info { info := base; info.GuestID = "guest"; return info }(), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := foregroundHumanSession(tc.info); got != tc.want {
				t.Fatalf("foregroundHumanSession() = %v, want %v", got, tc.want)
			}
		})
	}
}
