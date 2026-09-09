package qq

import (
	"context"
	"testing"

	"github.com/tencent-connect/botgo/dto"
	"github.com/tencent-connect/botgo/openapi"
	"github.com/tencent-connect/botgo/openapi/options"

	"github.com/CherryHQ/stella/pkg/channel"
)

type durableRouteAPI struct {
	openapi.OpenAPI
	group, user string
}

func (a *durableRouteAPI) PostGroupMessage(_ context.Context, target string, _ dto.APIMessage, _ ...options.Option) (*dto.Message, error) {
	a.group = target
	return &dto.Message{}, nil
}

func (a *durableRouteAPI) PostC2CMessage(_ context.Context, target string, _ dto.APIMessage, _ ...options.Option) (*dto.Message, error) {
	a.user = target
	return &dto.Message{}, nil
}

func TestDurablePublisherKeepsC2CAndGroupTargetsSeparate(t *testing.T) {
	for _, group := range []bool{false, true} {
		api := &durableRouteAPI{}
		bot := &Bot{api: api}
		events := make(chan channel.Event, 1)
		events <- channel.Event{Text: "recovered reply"}
		close(events)
		chatID := "qq:c2c:recipient"
		if group {
			chatID = "qq:group:room"
		}
		if err := bot.PublishIncoming(t.Context(), channel.DurablePublishRequest{
			IsGroup: group, ChatID: chatID, TargetID: "recipient", MessageID: "incoming",
			Stream: &channel.ChatStream{Events: events},
		}); err != nil {
			t.Fatal(err)
		}
		if group && (api.group != "room" || api.user != "") {
			t.Fatalf("group reply routed to group=%q user=%q", api.group, api.user)
		}
		if !group && (api.user != "recipient" || api.group != "") {
			t.Fatalf("C2C reply routed to group=%q user=%q", api.group, api.user)
		}
	}
}
