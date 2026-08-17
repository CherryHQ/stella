package telegram

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	tele "gopkg.in/telebot.v4"

	internalchannel "github.com/CherryHQ/stella/internal/channel"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// capturingHandler records what the adapter forwarded to the coordinator. It
// reports the command as unhandled so the bot never tries to reply over the
// network.
type capturingHandler struct {
	fakeChannelHandler
	command string
	args    string
	calls   int
}

func (h *capturingHandler) EnsurePlatformGroupMember(context.Context, string, string, string) error {
	return nil
}

type attachmentResolverHandler struct {
	fakeChannelHandler
	err error
}

func (h attachmentResolverHandler) AdmitAssetSave(context.Context, pkgchannel.IncomingMessage) error {
	return h.err
}

type telegramRequestCounter struct{ getFileCalls int }

func (r *telegramRequestCounter) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.Contains(req.URL.Path, "/getFile") {
		r.getFileCalls++
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewBufferString(`{"ok":true,"result":{}}`)),
		Request:    req,
	}, nil
}

func TestAttachmentResolutionErrorsDoNotFetchTelegramBytes(t *testing.T) {
	attachments := []struct {
		name string
		set  func(*tele.Message)
	}{
		{name: "photo", set: func(m *tele.Message) { m.Photo = &tele.Photo{File: tele.File{FileID: "photo-id"}} }},
		{name: "document", set: func(m *tele.Message) {
			m.Document = &tele.Document{File: tele.File{FileID: "document-id"}, FileName: "test.txt"}
		}},
		{name: "audio", set: func(m *tele.Message) { m.Audio = &tele.Audio{File: tele.File{FileID: "audio-id"}} }},
		{name: "video", set: func(m *tele.Message) { m.Video = &tele.Video{File: tele.File{FileID: "video-id"}} }},
		{name: "voice", set: func(m *tele.Message) { m.Voice = &tele.Voice{File: tele.File{FileID: "voice-id"}} }},
		{name: "video-note", set: func(m *tele.Message) {
			m.VideoNote = &tele.VideoNote{File: tele.File{FileID: "video-note-id"}}
		}},
		{name: "animation", set: func(m *tele.Message) { m.Animation = &tele.Animation{File: tele.File{FileID: "animation-id"}} }},
		{name: "sticker", set: func(m *tele.Message) { m.Sticker = &tele.Sticker{File: tele.File{FileID: "sticker-id"}} }},
	}
	for _, attachment := range attachments {
		for _, resolveErr := range []error{internalchannel.ErrAgentAccessDenied, errors.New("resolver unavailable")} {
			t.Run(attachment.name+"/"+resolveErr.Error(), func(t *testing.T) {
				requests := &telegramRequestCounter{}
				bot, err := tele.NewBot(tele.Settings{
					Offline: true, Synchronous: true, Token: "test",
					URL: "https://telegram.invalid", Client: &http.Client{Transport: requests},
				})
				if err != nil {
					t.Fatal(err)
				}
				b := &Bot{bot: bot, handler: attachmentResolverHandler{err: resolveErr}, ctx: context.Background(), cfg: Config{AllowDM: true}}
				b.registerHandlers()
				message := &tele.Message{ID: 42, Sender: &tele.User{ID: 7}, Chat: &tele.Chat{ID: 7, Type: tele.ChatPrivate}}
				attachment.set(message)

				bot.ProcessUpdate(tele.Update{Message: message})
				if requests.getFileCalls != 0 {
					t.Fatalf("Telegram getFile calls = %d, want 0", requests.getFileCalls)
				}
			})
		}
	}
}

func (h *capturingHandler) HandleIncoming(_ context.Context, _ pkgchannel.IncomingMessage, command, args string) (string, bool, *pkgchannel.ChatStream, error) {
	h.calls++
	h.command, h.args = command, args
	return "", false, nil, nil
}

// TestSharedCommandForwardsPayload proves a slash command's argument survives
// the Telegram adapter and reaches the coordinator verbatim. Telegram delivers
// the argument as a separate payload field, so an adapter that forwards only
// the command silently strips every argument the user typed.
func TestSharedCommandForwardsPayload(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want string
	}{
		{name: "with argument", text: "/new@stella_bot extra", want: "extra"},
		{name: "padded", text: "/new@stella_bot    extra   ", want: "extra"},
		{name: "bare", text: "/new@stella_bot", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bot, err := tele.NewBot(tele.Settings{Offline: true, Synchronous: true})
			if err != nil {
				t.Fatalf("offline bot: %v", err)
			}
			handler := &capturingHandler{}
			bot.Me = &tele.User{ID: 1, Username: "stella_bot"}
			b := &Bot{bot: bot, handler: handler, ctx: context.Background(), cfg: Config{AllowGroup: true, RequireMention: true}}
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

func TestTelegramIngressAdmission(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     Config
		message tele.Message
		want    bool
	}{
		{
			name: "direct messages disabled",
			cfg:  Config{AllowDM: false},
			message: tele.Message{
				Text: "hello", Sender: &tele.User{ID: 7},
				Chat: &tele.Chat{ID: 7, Type: tele.ChatPrivate},
			},
		},
		{
			name: "group chats disabled",
			cfg:  Config{AllowGroup: false, RequireMention: false},
			message: tele.Message{
				Text: "hello", Sender: &tele.User{ID: 7},
				Chat: &tele.Chat{ID: -100, Type: tele.ChatSuperGroup},
			},
		},
		{
			name: "allowed group requires mention",
			cfg:  Config{AllowGroup: true, RequireMention: true},
			message: tele.Message{
				Text: "hello", Sender: &tele.User{ID: 7},
				Chat: &tele.Chat{ID: -100, Type: tele.ChatSuperGroup},
			},
		},
		{
			name: "bare group command requires mention",
			cfg:  Config{AllowGroup: true, RequireMention: true},
			message: tele.Message{
				Text: "/new", Sender: &tele.User{ID: 7},
				Chat: &tele.Chat{ID: -100, Type: tele.ChatSuperGroup},
			},
		},
		{
			name: "targeted group command is addressed",
			cfg:  Config{AllowGroup: true, RequireMention: true},
			message: tele.Message{
				Text: "/new@stella_bot", Sender: &tele.User{ID: 7},
				Chat: &tele.Chat{ID: -100, Type: tele.ChatSuperGroup},
			},
			want: true,
		},
		{
			name: "allowed mentioned group",
			cfg:  Config{AllowGroup: true, RequireMention: true},
			message: tele.Message{
				Text: "@stella_bot hello", Entities: tele.Entities{{Type: tele.EntityMention, Offset: 0, Length: 11}}, Sender: &tele.User{ID: 7},
				Chat: &tele.Chat{ID: -100, Type: tele.ChatSuperGroup},
			},
			want: true,
		},
		{name: "reply to bot is directed", cfg: Config{AllowGroup: true, RequireMention: true}, message: tele.Message{Text: "follow up", Sender: &tele.User{ID: 7}, Chat: &tele.Chat{ID: -100, Type: tele.ChatSuperGroup}, ReplyTo: &tele.Message{Sender: &tele.User{ID: 1, Username: "stella_bot"}}}, want: true},
		{name: "reply to lookalike is not directed", cfg: Config{AllowGroup: true, RequireMention: true}, message: tele.Message{Text: "follow up", Sender: &tele.User{ID: 7}, Chat: &tele.Chat{ID: -100, Type: tele.ChatSuperGroup}, ReplyTo: &tele.Message{Sender: &tele.User{ID: 99, Username: "stella_bot"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bot, err := tele.NewBot(tele.Settings{Offline: true, Synchronous: true})
			if err != nil {
				t.Fatal(err)
			}
			bot.Me = &tele.User{ID: 1, Username: "stella_bot"}
			handler := &capturingHandler{}
			b := &Bot{bot: bot, handler: handler, ctx: context.Background(), cfg: tc.cfg}
			b.registerHandlers()
			tc.message.ID = 42
			bot.ProcessUpdate(tele.Update{Message: &tc.message})
			if got := handler.calls == 1; got != tc.want {
				t.Fatalf("message forwarded = %v, want %v", got, tc.want)
			}
		})
	}
}
