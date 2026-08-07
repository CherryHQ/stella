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

func (h attachmentResolverHandler) ResolveUserRoot(context.Context, pkgchannel.IncomingMessage) (string, error) {
	return "", h.err
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
	for _, attachment := range []string{"photo", "document"} {
		for _, resolveErr := range []error{internalchannel.ErrAgentAccessDenied, errors.New("resolver unavailable")} {
			t.Run(attachment+"/"+resolveErr.Error(), func(t *testing.T) {
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
				if attachment == "photo" {
					message.Photo = &tele.Photo{File: tele.File{FileID: "photo-id"}}
				} else {
					message.Document = &tele.Document{File: tele.File{FileID: "document-id"}, FileName: "test.txt"}
				}

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
		{name: "with argument", text: "/new extra", want: "extra"},
		{name: "padded", text: "/new    extra   ", want: "extra"},
		{name: "bare", text: "/new", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bot, err := tele.NewBot(tele.Settings{Offline: true, Synchronous: true})
			if err != nil {
				t.Fatalf("offline bot: %v", err)
			}
			handler := &capturingHandler{}
			b := &Bot{bot: bot, handler: handler, ctx: context.Background(), cfg: Config{AllowedChatIDs: "-100", RequireMention: true}}
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
			name: "group not allowlisted",
			cfg:  Config{AllowedChatIDs: "-200", RequireMention: false},
			message: tele.Message{
				Text: "hello", Sender: &tele.User{ID: 7},
				Chat: &tele.Chat{ID: -100, Type: tele.ChatSuperGroup},
			},
		},
		{
			name: "allowlisted group requires mention",
			cfg:  Config{AllowedChatIDs: "-100", RequireMention: true},
			message: tele.Message{
				Text: "hello", Sender: &tele.User{ID: 7},
				Chat: &tele.Chat{ID: -100, Type: tele.ChatSuperGroup},
			},
		},
		{
			name: "unknown slash command still requires mention",
			cfg:  Config{AllowedChatIDs: "-100", RequireMention: true},
			message: tele.Message{
				Text: "/anything", Sender: &tele.User{ID: 7},
				Chat: &tele.Chat{ID: -100, Type: tele.ChatSuperGroup},
			},
		},
		{
			name: "allowlisted mentioned group",
			cfg:  Config{AllowedChatIDs: "-100", RequireMention: true},
			message: tele.Message{
				Text: "@stella_bot hello", Entities: tele.Entities{{Type: tele.EntityMention, Offset: 0, Length: 11}}, Sender: &tele.User{ID: 7},
				Chat: &tele.Chat{ID: -100, Type: tele.ChatSuperGroup},
			},
			want: true,
		},
		{name: "reply to bot is directed", cfg: Config{AllowedChatIDs: "-100", RequireMention: true}, message: tele.Message{Text: "follow up", Sender: &tele.User{ID: 7}, Chat: &tele.Chat{ID: -100, Type: tele.ChatSuperGroup}, ReplyTo: &tele.Message{Sender: &tele.User{ID: 1, Username: "stella_bot"}}}, want: true},
		{name: "reply to lookalike is not directed", cfg: Config{AllowedChatIDs: "-100", RequireMention: true}, message: tele.Message{Text: "follow up", Sender: &tele.User{ID: 7}, Chat: &tele.Chat{ID: -100, Type: tele.ChatSuperGroup}, ReplyTo: &tele.Message{Sender: &tele.User{ID: 99, Username: "stella_bot"}}}},
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

func TestTelegramLocalAgentCommandRunsAdmissionFirst(t *testing.T) {
	for _, tc := range []struct {
		name          string
		handled       bool
		wantSwitch    int
		wantAdmission int
	}{
		{name: "guest is rejected", handled: true, wantAdmission: 1},
		{name: "linked user continues", handled: false, wantAdmission: 1, wantSwitch: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bot, err := tele.NewBot(tele.Settings{Offline: true, Synchronous: true})
			if err != nil {
				t.Fatalf("offline bot: %v", err)
			}
			handler := &telegramLocalCommandAdmissionHandler{handled: tc.handled}
			b := &Bot{bot: bot, handler: handler, ctx: context.Background(), cfg: Config{AllowDM: true}}
			b.registerHandlers()

			bot.ProcessUpdate(tele.Update{Message: &tele.Message{
				ID: 42, Text: "/agent other", Payload: "other",
				Sender: &tele.User{ID: 7}, Chat: &tele.Chat{ID: 7, Type: tele.ChatPrivate},
			}})

			if handler.admissionCalls != tc.wantAdmission || handler.switchCalls != tc.wantSwitch || handler.listCalls != 0 {
				t.Fatalf("calls: admission=%d switch=%d list=%d, want %d, %d, 0", handler.admissionCalls, handler.switchCalls, handler.listCalls, tc.wantAdmission, tc.wantSwitch)
			}
		})
	}
}

type telegramLocalCommandAdmissionHandler struct {
	fakeChannelHandler
	handled        bool
	admissionCalls int
	listCalls      int
	switchCalls    int
}

func (h *telegramLocalCommandAdmissionHandler) AdmitLocalCommand(context.Context, pkgchannel.IncomingMessage) (string, bool, error) {
	h.admissionCalls++
	return "This command is not available in guest chat.", h.handled, nil
}

func (h *telegramLocalCommandAdmissionHandler) ListAgents(context.Context, pkgchannel.IncomingMessage) ([]pkgchannel.AgentInfo, string, error) {
	h.listCalls++
	return nil, "", nil
}

func (h *telegramLocalCommandAdmissionHandler) SwitchAgent(context.Context, pkgchannel.IncomingMessage, string) error {
	h.switchCalls++
	return nil
}
