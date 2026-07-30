package telegram

import (
	"context"
	"testing"

	tele "gopkg.in/telebot.v4"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// capturingHandler records what the adapter forwarded to the coordinator. It
// reports the command as unhandled so the bot never tries to reply over the
// network.
type capturingHandler struct {
	fakeChannelHandler
	command string
	args    string
}

func (h *capturingHandler) HandleIncoming(_ context.Context, _ pkgchannel.IncomingMessage, command, args string) (string, bool, *pkgchannel.ChatStream, error) {
	h.command, h.args = command, args
	return "", false, nil, nil
}

// TestSharedCommandForwardsPayload proves a slash command's argument survives
// the Telegram adapter. In a multi-agent group `/new @agent-b` names the agent
// whose session to reset; forwarding an empty argument made that command
// ambiguous, so it could never reach the target the user typed.
func TestSharedCommandForwardsPayload(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want string
	}{
		{name: "targeted", text: "/new @agent-b", want: "@agent-b"},
		{name: "padded", text: "/new    @agent-b   ", want: "@agent-b"},
		{name: "bare", text: "/new", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bot, err := tele.NewBot(tele.Settings{Offline: true, Synchronous: true})
			if err != nil {
				t.Fatalf("offline bot: %v", err)
			}
			handler := &capturingHandler{}
			b := &Bot{bot: bot, handler: handler, ctx: context.Background()}
			b.registerHandlers()

			bot.ProcessUpdate(tele.Update{Message: &tele.Message{
				ID:     42,
				Text:   tc.text,
				Sender: &tele.User{ID: 7},
				Chat:   &tele.Chat{ID: -100, Type: tele.ChatSuperGroup},
			}})

			if handler.command != "/new" {
				t.Fatalf("command = %q, want /new", handler.command)
			}
			if handler.args != tc.want {
				t.Fatalf("args = %q, want %q", handler.args, tc.want)
			}
		})
	}
}
